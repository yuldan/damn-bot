//go:build !minimal

package main

import (
	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/i18n"
)

// replyLanguage resolves `[ui] reply_language` for the stable tier: auto adds
// no line, follow_ui uses the resolved UI language, and a code is itself.
func replyLanguage(cfg *config.Config) string {
	switch cfg.UI.ReplyLanguage {
	case "", "auto":
		return ""
	case "follow_ui":
		return i18n.Resolve(cfg.UI.Language)
	default:
		return i18n.Resolve(cfg.UI.ReplyLanguage)
	}
}

// stableReply builds the stable-tier options; the minimal build keeps this
// feature out of the binary (GAP-INF-09, P13).
func stableReply(cfg *config.Config) assemble.StableOptions {
	return assemble.StableOptions{ReplyLanguage: replyLanguage(cfg)}
}
