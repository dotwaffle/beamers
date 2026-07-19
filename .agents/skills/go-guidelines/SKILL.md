---
name: go-guidelines
description: Apply Beamers Go engineering rules when editing, generating, reviewing, testing, or refactoring Go code, go.mod, or go.sum. Use for every Go change in this repository; do not use for work unrelated to Go.
---

# Beamers Go Guidelines

Apply these rules to every Go change in this repository.
Treat required rules as the default review and CI contract.
Document a narrow exception when a required rule cannot reasonably apply.

## Language and dependencies

- Treat the `go` directive in `go.mod` as the language-version source of truth.
- Use syntax and standard-library APIs available in that version.
- Prefer modern Go when it improves clarity; do not modernize merely for novelty.
- Prefer the standard library.
- Add a dependency only when its maintenance, security, license, and transitive costs are justified by clear value.
- Use `go get` and `go mod tidy` for dependency changes; do not hand-edit `go.sum`.

## Design and style

- Run `gofmt` and `goimports`; keep `go vet` and the project lint configuration clean.
- Use American English in names, comments, errors, and documentation.
- Prefer small, readable functions and low cyclomatic complexity.
- Pass dependencies explicitly; avoid package globals and hidden initialization.
- Avoid package-name stutter, such as `kv.KVStore`; prefer `kv.Store`.
- Define small interfaces near consumers.
  Return concrete types unless callers need an abstraction.
- Prefer composition and ownership over framework-style inheritance or implicit registries.
- Pass ordinary values as direct parameters.
  Introduce a parameter struct only for a coherent configuration or option set, not merely because a function has several arguments.
- Use generics when they make a reusable invariant clearer.
  Avoid reflection on hot paths and where ordinary typed code is simpler.
- Keep generated code generated; change its source and regenerate it instead of editing generated files manually.

## Errors

- Handle every meaningful error.
  Add concise action context at the boundary where it helps diagnosis.
- Wrap an error with `%w` only when callers are allowed to inspect the cause as part of the API contract.
  Translate or keep causes opaque when exposing them would leak implementation details.
- Use `errors.Is`, `errors.AsType`, or `errors.As` for classification.
  Never branch on error text.
- Define sentinel errors only when callers need stable classification.
  Use typed errors when callers need structured details.
- Keep error strings lowercase, without trailing punctuation, unless they begin with a proper noun.
- Check deferred cleanup errors when they can affect correctness, especially flush, sync, and writable close operations.

## Context and concurrency

- Put `context.Context` first, never pass `nil`, and propagate cancellation, deadlines, and values across request boundaries.
- Do not store a context in project structs.
  Pass it to each operation that needs it.
- Give every goroutine an owner and a bounded termination path.
  Use context cancellation for request or operation lifetime; use another explicit lifecycle mechanism when it fits better.
- Only the sending side that knows no more values will arrive may close a channel.
  Coordinate closure when there are multiple senders; receivers must not guess.
- Synchronize shared mutable state with ownership confinement, mutexes, atomics, or channels.
  Verify concurrent code with the race detector.
- Use `errgroup` only when the work has shared, fail-fast cancellation semantics.
- Size buffered channels from a documented throughput or backpressure requirement, not as a speculative leak fix.

## Testing

- Keep tests deterministic and hermetic by default.
- Use table-driven tests only when cases share setup, behavior, and assertions.
  Prefer focused regression, fuzz, property, or golden tests when those forms express the behavior better.
- Use `t.Helper`, `t.Cleanup`, `t.TempDir`, `t.Setenv`, and `t.Context` where appropriate.
- Call `t.Parallel` only when isolation is proven and parallelism is useful.
- Run the race detector in CI.
  If its cost becomes material, document a deliberate split between pull-request, targeted, sharded, and scheduled race coverage.
- Test public behavior and failure paths; avoid tests coupled only to private implementation details.

## Logging and OpenTelemetry

- Use `log/slog` for application logging with stable field names and appropriate levels.
- Reusable packages must not log by default.
  Accept an explicit logger when logging is part of their contract.
- Use OpenTelemetry for traces and metrics from the start of the project.
- Initialize providers, resources, exporters, propagation, and shutdown at application boundaries.
  Reusable packages must not create telemetry SDK providers or exporters.
- Pass telemetry dependencies or narrow instrumentation helpers explicitly where practical; do not hide behavior behind mutable globals.
- Instrument service boundaries and meaningful operations, not every function.
- Propagate context, end every span, record errors deliberately, and use span status only when it adds semantic value.
- Use stable semantic conventions and low-cardinality metric attributes.
  Never record secrets or unnecessary personal data.
- Correlate logs with trace and span identifiers when it materially improves diagnosis.

## Performance

- Measure before optimizing with profiles, benchmarks, and representative workloads.
- Preserve clarity unless evidence shows an optimization matters.
- Add benchmarks for critical paths and use `benchstat` for comparisons.
- Reuse buffers or add pooling only after measuring allocation pressure and checking ownership carefully.

## Configuration

- Read flags, environment variables, files, or remote configuration at application boundaries.
- Convert configuration into typed values, validate it before startup completes, and pass it explicitly.
- Treat configuration as immutable after initialization unless hot reload is a deliberate, synchronized, and tested feature.
- Provide safe defaults and actionable validation errors.

## APIs and security

- Keep exported APIs minimal and document exported identifiers with useful contracts rather than restating their names.
- Validate untrusted input at system boundaries and fuzz parsers or decoders exposed to hostile data.
- Set deadlines or timeouts for external I/O according to the protocol and trust boundary.
  Document deliberate exceptions such as streaming or local IPC.
- Use TLS where the trust boundary requires it; do not make universal transport assumptions inside reusable packages.
- Never log secrets.
  Keep credentials outside source control and grant the least filesystem and network access needed.
- Use functional options only for genuinely optional, extensible constructor behavior.
  Prefer a typed config for required settings.

## Tooling gates

- Pin the Go toolchain and development tools in `mise.toml` and update pins deliberately.
- Before completing a Go change, run `golangci-lint fmt`, `go vet ./...`, `golangci-lint run`, `go test -race ./...`, and `go build ./...` as applicable.
- Run `govulncheck ./...` for dependency updates and in CI.
- Keep `//nolint` directives specific, locally justified, and exceptional.
- Use `-trimpath` for release builds to remove local paths, but do not claim reproducibility from that flag alone.
  Pin inputs and control embedded metadata as part of the release process.
