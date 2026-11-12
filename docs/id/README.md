<!-- translation: source=../README.md sha256=a5899c8f4e8c78a944b432752c22013f31e8a2b3b199668d69d96983d64ff4b5 -->
# Damn-Bot documentation

Documentation index. The normative specification is [SPEC.md](../../SPEC.md); ADRs
live in [DECISIONS.md](../DECISIONS.md).

## Getting started

- [VPS.md](../VPS.md) — server install, service, and day-two operations.
- [CONFIG-REFERENCE.md](../CONFIG-REFERENCE.md) — generated configuration reference.
- [reference/CLI.md](../reference/CLI.md) — generated commands and flags.
- [reference/ENVIRONMENT.md](../reference/ENVIRONMENT.md) — config and container env.
- [reference/DEPLOYMENT.md](../reference/DEPLOYMENT.md) — entrypoint, unit, bind.

## Architecture and guarantees

- [ENGINEERING.md](../ENGINEERING.md) — gates, budgets, and definition of done.
- [TRUST-MODEL.md](../TRUST-MODEL.md) — threat model and trust boundaries.
- [CROSS-CUTTING.md](../CROSS-CUTTING.md) — cross-module contracts.
- [SELF-HEALING.md](../SELF-HEALING.md) — supervisor and recovery.

## Generated references

Pages under `docs/reference/` are generated from code by
`go generate ./cmd/damnbot` and checked by `TestCLIDocsMatchHelp`,
`TestConfigDocSync`, `TestEnvDocSync`, and `TestDeploymentDocSync`. Never edit
them by hand: change the code and regenerate.

## Translations

Translations live under `docs/<locale>/`. Every page must carry a first-line
banner

```text
<!-- translation: source=../README.md sha256=<sha256 of the source page> -->
```

`TestTranslationBannerFresh` recomputes the source sha; a mismatch is reported
as `STALE` and fails the gate. `docs/id/` is the example translation of this
page.
