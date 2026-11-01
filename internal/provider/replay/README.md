# Provider fixture replay

This package is the CI gate for the provider catalog (SPEC §4.1,
[`docs/PROVIDERS.md`](../../../docs/PROVIDERS.md)). It is test-only: no
production package imports it, and the `!minimal` build tag keeps it out of
the minimal binary.

`catalog_test.go` iterates every row of the embedded
`internal/provider/registry/providers.toml` and drives the row through the
real wire adapter it names (`openai-chat`, `openai-responses`,
`anthropic-messages`, `gemini`) over a loopback HTTP listener. There is no
network access and no per-provider test data file: the model name, endpoint
path, auth header, request-body shape, and response stream are derived from
the catalog row and the protocol fixture.

Each wired row is exercised three times:

- **success** — text, finish reason, model, and input/output/cached usage;
- **400** — typed `provider.Error` with class `bad_request`, sent once;
- **429 → 200** — one retry on the same route, then success.

`TestCatalogReplayBaseURLs` validates every default `base_url` (including
documented `{region}`/`<account>` placeholders after substitution) and
replays rows whose `base_url` is intentionally user-supplied.
`TestCatalogReplayCoverage` accounts for all 88 catalog rows: currently **85
wired + 3 fail-closed** (`bedrock`, `cursor`, `factory-droid`, whose
protocols have no adapter and must fail at construction rather than reach the
wrong encoder).

## Gate boundary

CI uses this replay suite as the provider gate. A real-network smoke of at
least 10 major providers is **not** part of CI: credentials are unavailable
there, so such a smoke must be opt-in and must not be reported as passing
unless it actually ran with real credentials.

## Opt-in real-provider smoke

`realprovider_test.go` (build tag `realprovider`) is **not** part of CI's
provider gate. It builds `cmd/damnbot` and asks each of 10 major providers for
a one-word answer; the child process reads credentials from the catalog env
candidates and the harness never prints a value.

```sh
DAMNBOT_REAL_PROVIDER=1 go test -tags realprovider ./internal/provider/replay -run TestRealProviders -v
```

Without `DAMNBOT_REAL_PROVIDER=1` the test skips. A CI job that declares it
holds provider keys must set `DAMNBOT_REAL_PROVIDER_REQUIRED=1`; then a
missing gate or credential is a **failure**, never a skip, so a green job
cannot claim a smoke that did not run.
