package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"regexp"
	"strings"

	"github.com/damnbot/damnbot/internal/i18n"
)

// a11yViolation is one audit finding with an axe-like impact level.
type a11yViolation struct {
	Rule, Impact, Detail string
}

var (
	btnTag    = regexp.MustCompile(`<button([^>]*)>`)
	inputTag  = regexp.MustCompile(`<(?:input|select|textarea)([^>]*)>`)
	labelFor  = regexp.MustCompile(`<label[^>]*for="([^"]+)"`)
	idAttr    = regexp.MustCompile(`id="([^"]+)"`)
	ariaLabel = regexp.MustCompile(`aria-label="[^"]+"`)
	imgTag    = regexp.MustCompile(`<img([^>]*)>`)
	imgAlt    = regexp.MustCompile(`alt="[^"]*"`)
	tabIndex  = regexp.MustCompile(`tabindex="([1-9][0-9]*)"`)
)

// auditPages applies a deterministic subset of the axe-core rules that can be
// checked without a browser: labels, button types, alt text, landmarks,
// lang/dir, focus trap, reduced motion and touch targets. Only serious and
// critical findings fail the gate (GAP-UX-07).
func auditPages(pages map[string]string, css, js string) []a11yViolation {
	var out []a11yViolation
	for name, body := range pages {
		out = append(out, auditPage(name, body)...)
	}
	return append(out, auditAssets(css, js)...)
}

// auditPage checks the landmarks, heading and button rules of one page.
func auditPage(name, body string) []a11yViolation {
	var out []a11yViolation
	if !strings.Contains(body, `lang="`) || !strings.Contains(body, `dir="`) {
		out = append(out, a11yViolation{"html-has-lang", "serious", name})
	}
	if strings.Count(body, "<main") != 1 {
		out = append(out, a11yViolation{"landmark-one-main", "serious", name})
	}
	if strings.Count(body, "<h1") != 1 {
		out = append(out, a11yViolation{"page-has-heading-one", "serious", name})
	}
	for _, m := range btnTag.FindAllStringSubmatch(body, -1) {
		if !strings.Contains(m[1], `type="`) {
			out = append(out, a11yViolation{"button-has-type", "critical", name})
		}
	}
	out = append(out, auditLabels(name, body)...)
	if tabIndex.MatchString(body) {
		out = append(out, a11yViolation{"tabindex", "serious", name})
	}
	if strings.Contains(body, `aria-modal="true"`) && !strings.Contains(body, `role="dialog"`) {
		out = append(out, a11yViolation{"aria-dialog-name", "serious", name})
	}
	return out
}

// auditLabels checks form labels and image alt text against the page ids.
func auditLabels(name, body string) []a11yViolation {
	var out []a11yViolation
	labels := map[string]bool{}
	for _, m := range labelFor.FindAllStringSubmatch(body, -1) {
		labels[m[1]] = true
	}
	ids := map[string]bool{}
	for _, m := range idAttr.FindAllStringSubmatch(body, -1) {
		ids[m[1]] = true
	}
	for _, m := range inputTag.FindAllStringSubmatch(body, -1) {
		if !ariaLabel.MatchString(m[1]) && !strings.Contains(m[1], `type="hidden"`) {
			hasLabel := false
			for id := range ids {
				if labels[id] && strings.Contains(m[1], `id="`+id+`"`) {
					hasLabel = true
				}
			}
			if !hasLabel {
				out = append(out, a11yViolation{"label", "critical", name})
			}
		}
	}
	for _, m := range imgTag.FindAllStringSubmatch(body, -1) {
		if !imgAlt.MatchString(m[1]) {
			out = append(out, a11yViolation{"image-alt", "critical", name})
		}
	}
	return out
}

// auditAssets checks the embedded stylesheet and script rules.
func auditAssets(css, js string) []a11yViolation {
	var out []a11yViolation
	for _, want := range []string{":focus-visible", "prefers-reduced-motion", "min-height: 44px"} {
		if !strings.Contains(css, want) {
			out = append(out, a11yViolation{"focus-visible", "serious", "app.css"})
		}
	}
	for _, want := range []string{`"Escape"`, `"Tab"`, ".focus()", `createElement("article")`} {
		if !strings.Contains(js, want) {
			out = append(out, a11yViolation{"keyboard-trap", "serious", "app.js"})
		}
	}
	return out
}

// auditEmbedded runs the audit over every rendered page (18 locales) and the
// embedded script/style.
func auditEmbedded() ([]a11yViolation, error) {
	assets, err := defaultAssets()
	if err != nil {
		return nil, err
	}
	css, err := fs.ReadFile(assets, "app.css")
	if err != nil {
		return nil, fmt.Errorf("web: read css: %w", err)
	}
	js, err := fs.ReadFile(assets, "app.js")
	if err != nil {
		return nil, fmt.Errorf("web: read js: %w", err)
	}
	pages := map[string]string{}
	for _, locale := range i18n.Locales() {
		tmpl, terr := parseTemplates(assets)
		if terr != nil {
			return nil, fmt.Errorf("web: parse templates %s: %w", locale, terr)
		}
		lang := i18n.Resolve(locale)
		data := &pageData{CSRF: "csrf", Lang: lang, Dir: dirOf(lang), Language: lang, Locales: i18n.Locales()}
		for name, t := range map[string]*template.Template{"index": tmpl.index, "settings": tmpl.settings, "auth": tmpl.auth} {
			var b strings.Builder
			if terr := t.Execute(&b, data); terr != nil {
				return nil, fmt.Errorf("web: render %s/%s: %w", name, locale, terr)
			}
			pages[name+":"+locale] = b.String()
		}
	}
	return auditPages(pages, string(css), string(js)), nil
}

func severe(violations []a11yViolation) []a11yViolation {
	var out []a11yViolation
	for _, v := range violations {
		if v.Impact == "serious" || v.Impact == "critical" {
			out = append(out, v)
		}
	}
	return out
}
