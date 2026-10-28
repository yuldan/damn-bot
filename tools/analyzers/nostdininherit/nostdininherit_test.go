package nostdininherit_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/damnbot/damnbot/tools/analyzers/nostdininherit"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), nostdininherit.Analyzer, "example.com/spawn")
}
