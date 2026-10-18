// Command damnbot-vet bundles the project's custom analyzers (ENGINEERING
// §5.1) for use as `go vet -vettool=$(which damnbot-vet) ./...`.
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/damnbot/damnbot/tools/analyzers/nohttpclient"
)

func main() {
	unitchecker.Main(
		nohttpclient.Analyzer,
	)
}
