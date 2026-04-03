# AGENTS.md

This file is the working guide for coding agents in this repository.
It is based on the current Go codebase, the GitHub workflows, and the checked-in configs.
If another instruction source disagrees with this file, prefer explicit user instructions first, then this file, then generic defaults.

## Rule files present in this repo

- `AGENTS.md`: this file.
- `.cursor/rules/`: not present.
- `.cursorrules`: not present.
- `.github/copilot-instructions.md`: not present.

There is no separate Cursor or Copilot rule set to merge in today.

## Project summary

This repository is a Go service that exports cloud metrics for Kubernetes nodes.
The Phase 1 implementation is Aliyun-focused and exposes Prometheus metrics on `/metrics`.
The binary entrypoint lives in `cmd/cloud-metrics-exporter`.
Most implementation code lives under `internal/`.

## Repository map

- `cmd/cloud-metrics-exporter/main.go`: binary entrypoint and process wiring.
- `internal/config/`: YAML config loading and validation.
- `internal/discovery/`: Kubernetes node discovery and volume enrichment.
- `internal/exporter/`: Prometheus collector implementation.
- `internal/metrics/`: metric definitions and raw-name helpers.
- `internal/provider/`: provider interface.
- `internal/provider/aliyun/`: Aliyun implementation.
- `configs/config.yaml`: sample runtime config.
- `configs/prometheus-scrape.yaml`: Prometheus scrape example.
- `deploy/`: Kubernetes manifests.
- `.github/workflows/`: CI/CD workflows.

## Source-of-truth commands

These commands are directly evidenced by `README.md` and `.github/workflows/_verify.yml`.

### Build

- Local binary build: `go build ./cmd/cloud-metrics-exporter`
- CI Linux build: `mkdir -p bin && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/cloud-metrics-exporter ./cmd/cloud-metrics-exporter`

### Test

- Full test suite: `go test ./...`
- Single package test run: `go test ./internal/config`
- Single test run in one package: `go test ./internal/config -run '^TestLoad$'`

Use the standard Go `-run` filter for focused testing.
The repo does not define a custom wrapper for single-test execution.

### Docker smoke build

`docker build .`

### Lint / formatting

There is no dedicated lint command or lint config checked into this repo.
No `Makefile`, `golangci-lint` config, or repo-specific formatter config was found.
For style verification, rely on standard Go formatting expectations, LSP diagnostics, and `go test ./...`.

## Daily development workflow for agents

1. Read the relevant package and its nearby tests before changing code.
2. Keep changes narrow and consistent with the surrounding package.
3. Run the most specific test first, then `go test ./...` for non-trivial changes.
4. Run `go build ./cmd/cloud-metrics-exporter` for wiring changes and `docker build .` when container behavior changed.

## Code style guidelines

### Formatting and imports

- Follow standard Go formatting.
- Keep files `gofmt`-clean.
- Do not introduce custom formatting conventions.
- Prefer normal package names with no alias when the imported name is already clear.
- Use an alias only when it avoids a collision or meaningfully improves readability.
- Existing justified aliases include `projectmetrics` for `internal/metrics` and `aliyunprovider` for `internal/provider/aliyun`.
- Keep standard-library imports separated from third-party and local imports, as Go tooling does.

### Package structure

- Keep new non-exported application code under `internal/`.
- Put binary startup wiring in `cmd/cloud-metrics-exporter/main.go` or nearby package helpers.
- Do not create broad utility packages unless the repo clearly needs them.

### Types

- Prefer concrete structs and small interfaces, matching the current codebase.
- Keep interfaces near the code that consumes them when they are local abstractions.
- Use typed structs for config, targets, samples, and enrichments rather than loose data shapes.
- Do not bypass type safety.

### Naming and errors

- Use Go naming conventions consistently:
  - exported identifiers: PascalCase,
  - unexported identifiers: camelCase,
  - package names: short lowercase,
  - filenames: lowercase, often `snake_case.go` for multiword concepts.
- Name constructors as `NewX` when returning a new instance, as in `NewNodeDiscovery`.
- Name sentinel errors as `Err...`.
- Prefer names that reflect the cloud-metrics domain, not generic placeholders.
- Prefer returning errors over panicking.
- Wrap contextual failures with `fmt.Errorf("context: %w", err)` when adding useful call-site detail.
- Reuse sentinel errors for stable comparisons when callers or tests need `errors.Is`.
- Follow existing patterns: config validation returns sentinel errors like `ErrNoEnabledProvider`, while operational failures usually wrap with `%w`.
- At process startup in `main`, fatal startup failures are logged with `log.Fatalf`.
- In collectors and long-running polling paths, prefer logging and partial progress over crashing the process when that matches existing behavior.

### Logging

- Use the standard library `log` package unless the repo adopts a different logger later.
- Keep log messages operational and specific.
- Include component context in the message text when useful, for example `collector:` or `aliyun:`.
- Include key dimensions when they aid debugging, such as region, instance ID, node name, or sample counts.

### Context, timeouts, and concurrency

- Pass `context.Context` through I/O and provider boundaries.
- Use bounded contexts for scrape-time operations, consistent with `context.WithTimeout` in the exporter.
- Do not add calls that ignore cancellation without a strong reason.
- Protect shared mutable state with the appropriate mutex, matching current discovery/provider patterns.
- Prefer simple concurrency over clever concurrency.

### Testing conventions

- Put tests next to the code in `*_test.go` files.
- Keep tests in the same package as the implementation unless there is a strong reason not to.
- Use `t.Parallel()` where the test is safe to run concurrently.
- Prefer table-driven tests for validation and parser logic.
- Use `t.Run` with descriptive case names.
- Use `t.Fatalf` or `t.Fatal` for clear failures.
- Use `errors.Is` when checking sentinel errors.
- Prefer local fakes and stubs over heavy mocking frameworks.
- Follow existing examples such as fake Kubernetes clientsets in discovery tests and stub providers in exporter tests.

### Config and YAML changes

- Keep YAML keys aligned with the struct tags in `internal/config/config.go`.
- Preserve Go duration string values such as `60s` and `30s`.
- Keep the sample config in `configs/config.yaml` consistent with the deployed mount path `/config/config.yaml`.
- If config schema changes, update code, tests, and sample YAML together.

### Metrics-specific guidance

- Keep metric naming consistent with the definitions in `internal/metrics/defs.go`.
- Preserve the distinction between standard metrics and raw fallback metrics.
- Do not silently change label shape for existing exported metrics.
- Be aware that enriched disk labels differ from the base network label set.

## CI expectations

The reusable verify workflow currently checks `go test ./...`, a Linux build for `./cmd/cloud-metrics-exporter`, and `docker build .`.

Agents should treat those as the baseline compatibility target.

## Deployment notes

- The deployment manifest expects an image tag in the form `<short-git-hash>`.
- The service exposes the `metrics` port on `9100`.
- The sample Prometheus config keeps only the `cloud-metrics-exporter` service and scrapes `/metrics`.

## When editing this repo

- Match the surrounding package style before introducing a new pattern.
- Prefer minimal diffs for bug fixes.
- Do not add new dependencies without a clear need.
- Do not commit built binaries, local artifacts, or secrets.
- Do not invent repo-local commands that are not backed by checked-in files.
- If you document a command that is standard Go rather than repo-specific, say so explicitly.
- If you change conventions, update this file in the same change.
