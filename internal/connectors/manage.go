package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
	plugcatalog "github.com/damnbot/damnbot/internal/plugins/catalog"
)

// InstallRow is one catalog row offered to the approval card.
type InstallRow struct {
	Kind string
	ID   string
	Risk string
}

// Installer is the host surface that draws the catalog approval card and
// installs the rows the user approved. It is never called automatically and
// a nil installer fails closed with module_unavailable.
type Installer interface {
	Install(ctx context.Context, rows []InstallRow) ([]string, error)
}

var riskTerms = regexp.MustCompile(`(?i)terms of service|published api terms|third-party agents terms`)

func risk(desc string) string {
	if riskTerms.MatchString(desc) {
		return "high"
	}
	return "low"
}

type catalogArgs struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Query  string `json:"query"`
	Reason string `json:"reason"`
	Ack    string `json:"ack"`
	Items  []struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"items"`
}

type connectionsArgs struct {
	Action    string `json:"action"`
	Connector string `json:"connector"`
	Account   string `json:"account"`
	Label     string `json:"label"`
	TokenRef  string `json:"token_ref"`
}

func (m *Module) manage(ctx context.Context, tool string, raw json.RawMessage, env host.Env) (host.Result, error) {
	switch tool {
	case "manage_catalog":
		return m.manageCatalog(ctx, raw, env)
	case "manage_connections":
		return m.manageConnections(raw)
	}
	return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "unknown connector management tool " + tool}
}

func (m *Module) manageCatalog(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	origin := ""
	if env.Session.Valid() {
		origin = env.Session.Origin()
	}
	if origin != "web" && origin != "desktop" {
		return host.Result{}, &Error{Code: codeDenied, Message: "manage_catalog is available only in a desktop or web session"}
	}
	if m.catalog == nil {
		return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "plugin catalog is not available in this build"}
	}
	var a catalogArgs
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	switch a.Action {
	case "search":
		return m.catalogSearch(&a)
	case "install":
		return m.catalogInstall(ctx, &a)
	}
	return host.Result{}, &Error{Code: codeInvalidArgs, Message: "action must be search or install"}
}

func (m *Module) catalogSearch(a *catalogArgs) (host.Result, error) {
	query := strings.ToLower(strings.TrimSpace(a.Query))
	if a.Kind != "" && a.Kind != "plugin" && a.Kind != "skill" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "kind must be plugin or skill"}
	}
	var lines []string
	for _, e := range m.catalog.Entries() {
		switch a.Kind {
		case "skill":
			if e.Kind != plugcatalog.KindSkill {
				continue
			}
		case "plugin":
			if e.Kind == plugcatalog.KindSkill {
				continue
			}
		}
		if query != "" && !strings.Contains(strings.ToLower(e.ID), query) &&
			!strings.Contains(strings.ToLower(e.Description), query) {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\ttos_risk=%s", e.ID, e.Kind, e.Category, risk(e.Description)))
		if len(lines) == 10 {
			break
		}
	}
	if len(lines) == 0 {
		return host.Result{Content: "no catalog entries matched"}, nil
	}
	return host.Result{Content: strings.Join(lines, "\n")}, nil
}

func (m *Module) catalogInstall(ctx context.Context, a *catalogArgs) (host.Result, error) {
	if len(a.Items) == 0 || len(a.Items) > 20 {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "install needs 1..20 items"}
	}
	rows := make([]InstallRow, 0, len(a.Items))
	var high []string
	seen := map[string]bool{}
	for _, item := range a.Items {
		if item.Kind != "plugin" && item.Kind != "skill" {
			return host.Result{}, &Error{Code: codeInvalidArgs, Message: "item kind must be plugin or skill"}
		}
		key := item.Kind + "/" + item.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		e, ok := m.catalog.Lookup(item.ID)
		if !ok {
			return host.Result{}, &Error{Code: codeNotFound, Message: "catalog entry " + item.ID + " does not exist"}
		}
		if (item.Kind == "skill") != (e.Kind == plugcatalog.KindSkill) {
			return host.Result{}, &Error{Code: codeInvalidArgs, Message: "item " + item.ID + " is not a " + item.Kind}
		}
		r := InstallRow{Kind: item.Kind, ID: e.ID, Risk: risk(e.Description)}
		if r.Risk == "high" {
			high = append(high, e.ID)
		}
		rows = append(rows, r)
	}
	if len(high) > 0 {
		sort.Strings(high)
		want := strings.Join(high, ",")
		if strings.TrimSpace(a.Ack) != want {
			return host.Result{}, &Error{
				Code:    codeDenied,
				Message: "high-risk catalog entries need a typed acknowledgement",
				Hint:    "pass ack=" + want,
			}
		}
	}
	if m.installer == nil {
		return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "no catalog installer is wired into this session"}
	}
	installed, err := m.installer.Install(ctx, rows)
	if err != nil {
		return host.Result{}, AsHostError(err)
	}
	return host.Result{Content: "requested " + strings.Join(installed, ",")}, nil
}

func (m *Module) manageConnections(raw json.RawMessage) (host.Result, error) {
	if m.accounts == nil {
		return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "hosted connector service is not wired into this build"}
	}
	var a connectionsArgs
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	if a.Action == "" {
		a.Action = "status"
	}
	switch a.Action {
	case "status":
		return m.connectionsStatus(&a), nil
	case "connect", "rename", "disconnect":
		return m.connectionsMutate(&a)
	case "link":
		return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "no hosted authorization service is wired into this build"}
	}
	return host.Result{}, &Error{Code: codeInvalidArgs, Message: "unknown action " + a.Action}
}

func (m *Module) connectionsStatus(a *connectionsArgs) host.Result {
	accounts := m.accounts.List(a.Connector)
	lines := make([]string, 0, len(accounts))
	for _, acc := range accounts {
		lines = append(lines, fmt.Sprintf("%s/%s label=%s token=keychain", acc.Connector, acc.ID, acc.Label))
	}
	return host.Result{Content: strings.Join(lines, "\n")}
}

func (m *Module) connectionsMutate(a *connectionsArgs) (host.Result, error) {
	switch a.Action {
	case "connect":
		if a.Connector == "" || a.Account == "" {
			return host.Result{}, &Error{Code: codeInvalidArgs, Message: "connect needs connector and account"}
		}
		label := a.Label
		if label == "" {
			label = a.Account
		}
		tokenRef := a.TokenRef
		if tokenRef == "" {
			tokenRef = "keychain:connectors/" + a.Connector + "/" + a.Account
		}
		if _, err := m.accounts.Add(a.Connector, a.Account, label, tokenRef); err != nil {
			return host.Result{}, &Error{Code: codeInvalidArgs, Message: err.Error()}
		}
		return host.Result{Content: "connected " + a.Connector + "/" + a.Account + " token=keychain"}, nil
	case "rename":
		if a.Label == "" {
			return host.Result{}, &Error{Code: codeInvalidArgs, Message: "rename needs label"}
		}
		if err := m.accounts.Rename(a.Connector, a.Account, a.Label); err != nil {
			return host.Result{}, &Error{Code: codeNotFound, Message: err.Error()}
		}
		return host.Result{Content: "renamed " + a.Connector + "/" + a.Account}, nil
	case "disconnect":
		if err := m.accounts.Remove(a.Connector, a.Account); err != nil {
			return host.Result{}, &Error{Code: codeNotFound, Message: err.Error()}
		}
		return host.Result{Content: "disconnected " + a.Connector + "/" + a.Account}, nil
	}
	return host.Result{}, &Error{Code: codeInvalidArgs, Message: "unknown action " + a.Action}
}

// managementTools returns the two management catalog entries. They are part
// of the connectors module because upstream ships both in
// tools/connectors/tool.py; toolset selection still gates their exposure.
func managementTools() []Tool {
	return []Tool{
		{
			Name: "manage_catalog", Description: "Search the plugin catalog and request installs through the desktop approval card",
			Effect: "dynamic", Tags: []string{"plugins", "skills", "install", "catalog"},
			Schema: object(`"action":{"type":"string","enum":["search","install"]},"kind":{"type":"string","enum":["plugin","skill"]},"query":{"type":"string","maxLength":200},"reason":{"type":"string","maxLength":500},"ack":{"type":"string","maxLength":200},"items":{"type":"array","maxItems":20,"items":{"type":"object","properties":{"kind":{"type":"string","enum":["plugin","skill"]},"id":{"type":"string","minLength":1,"maxLength":64}},"required":["kind","id"],"additionalProperties":false}}`, "action"),
		},
		{
			Name: "manage_connections", Description: "Manage hosted connector accounts and their vault references",
			Effect: "dynamic", Tags: []string{"connectors", "accounts", "oauth", "vault"},
			Schema: object(`"action":{"type":"string","enum":["status","connect","rename","disconnect","link"]},"connector":{"type":"string","maxLength":64},"account":{"type":"string","maxLength":64},"label":{"type":"string","maxLength":128},"token_ref":{"type":"string","maxLength":256}`, "action"),
		},
	}
}
