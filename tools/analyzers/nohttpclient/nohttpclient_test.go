package nohttpclient_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/damnbot/damnbot/tools/analyzers/nohttpclient"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), nohttpclient.Analyzer,
		"github.com/damnbot/damnbot/internal/bad",
		"github.com/damnbot/damnbot/internal/good",
		"github.com/damnbot/damnbot/internal/netx",
		"github.com/damnbot/damnbot/internal/netx/dialer",
		"github.com/damnbot/damnbot/internal/netxlookalike",
		"example.com/other",
	)
}
