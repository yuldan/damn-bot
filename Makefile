# Damn-Bot — titik masuk build/gate (ENGINEERING §5, E-39).
# Tag build: satu daftar TAGS; target lain hanya menambah (TAGS += …). Tidak pernah lewat GOFLAGS
# (pengecualian: go-licenses tidak punya flag -tags, lihat target licenses).
TAGS ?= goolm

empty :=
space := $(empty) $(empty)
comma := ,
TAGSCSV = $(subst $(space),$(comma),$(strip $(TAGS)))
GOTAGS = -tags "$(TAGSCSV)"

# Tool dev dipin di tools/go.mod (E-45: di luar budget go.mod utama).
GOTOOL := go tool -modfile=tools/go.mod
LICENSES_ALLOWED := MIT,BSD-2-Clause,BSD-3-Clause,Apache-2.0,ISC,MPL-2.0

.PHONY: all build lint test size-check loc loc-ratio deps-check vuln licenses check meta-test bench-budget

all: check

build:
	CGO_ENABLED=0 go build $(GOTAGS) ./...

lint:
	$(GOTOOL) golangci-lint run --build-tags "$(TAGSCSV)" ./...

test:
	go test $(GOTAGS) -race -shuffle=on ./...

size-check:
	TAGS="$(TAGSCSV)" scripts/size-check.sh

loc:
	SCC="$(GOTOOL) scc" scripts/loc-check.sh budget

loc-ratio:
	SCC="$(GOTOOL) scc" scripts/loc-check.sh ratio

deps-check:
	go test $(GOTAGS) -count=1 -run 'TestDirectDeps|TestDirectRequiresParser' ./internal/archtest

vuln:
	$(GOTOOL) govulncheck $(GOTAGS) ./...

# go-licenses tidak punya flag -tags; satu-satunya jalur adalah GOFLAGS (tanpa -tags lain di command line,
# jadi tidak ada penimpaan E-39).
licenses:
	GOFLAGS=-tags=$(TAGSCSV) $(GOTOOL) go-licenses check ./... --allowed_licenses=$(LICENSES_ALLOWED)

check: lint test size-check loc deps-check vuln licenses

# Meta-test gate CI (ROADMAP F0): setiap gate harus gagal pada pelanggaran sintetis.
meta-test:
	scripts/ci-meta-test.sh

# Harness token P1 (bench/go.mod terpisah, di luar E-45): tokenizer o200k_base = tiktoken,
# reproduksi BASELINE-hermes.md ±5%.
bench-budget:
	cd bench && go test -count=1 ./budget/...
