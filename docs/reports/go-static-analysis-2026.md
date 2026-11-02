# Go static analysis baseline (2026)

**Verified:** 2026-11-01 against the official release and configuration sources linked below.

## Toolchain

| Component | Pinned version | Where it is pinned |
| --- | --- | --- |
| Go | 1.27.3 | `go.mod`; also used by CI and `just`. |
| golangci-lint | 2.15.0 | `justfile`, including the installer SHA-256. |
| govulncheck | 1.9.0 | `justfile`. |

Go 1.27.3 was the latest stable Go release when this baseline was established. These are reproducible pins, not floating `latest` references. Recheck upstream releases when updating them.

The CLI uses Cobra for command dispatch, flags, help, and completion. The loading boundary now uses Compose SDK v5.6.1 with its release-compatible Docker CLI v29.8.2, Moby client v0.6.1, and compose-go v2.16.1. The SDK webpage's older Docker CLI v28 compatibility note does not match this release's module graph. YAML decoding uses the SDK-compatible go-yaml v4.0.0-rc.6 API. `golang.org/x/net` is raised to v0.60.0 to address the vulnerability scan's HTTP/2 advisories; tests and scanning pass with that dependency set. Do not add unused runtime packages simply to populate the planned architecture.

## Recommended policy

Use golangci-lint v2 with the standard correctness checks and a small set relevant to a CLI that controls processes, files, networks, and Docker. Enabling every linter introduces contradictory style rules and makes updates unpredictable; it is not a stronger correctness guarantee.

The configured checks are:

| Checks | Purpose |
| --- | --- |
| `govet`, `staticcheck` | Suspicious language/API usage, correctness, and simplification checks. |
| `errcheck` | Unchecked errors and type assertions, including errors explicitly assigned to the blank identifier. |
| `ineffassign`, `unused` | Ineffective assignments and unreachable-from-use declarations. |
| `errorlint`, `nilerr` | Incorrect handling of wrapped errors and suspicious nil returns after errors. |
| `contextcheck` | Lost context propagation across calls. This does not prove timeout or cancellation behavior. |
| `bodyclose` | HTTP response bodies left unclosed. |
| `durationcheck` | Accidental multiplication of two durations. |
| `gosec`, `bidichk` | Security-sensitive coding patterns and misleading bidirectional Unicode controls. |
| `modernize` | Go's modernize analyzers for current language and standard-library idioms. |
| `nolintlint` | Specific, explained suppressions; unused suppressions remain errors. |

Keep tests under analysis. There are no blanket test exclusions, issue-count truncation, or new-code-only filters. Generated-code exclusion uses the standard strict generated-file marker. A legitimate false positive should get the narrowest specific suppression with a reason, not a package-wide bypass.

Do not add arbitrary function-length, complexity, naming-length, duplication, or mandatory-parallel-test rules to the initial gate. Add a rule only when it catches a demonstrated project defect or enforces a deliberate contract.

## Formatting

Use `gofumpt` for consistent Go-compatible formatting and `goimports` for imports, with this module's imports grouped separately. They run through golangci-lint's v2 `formatters` configuration.

- `just fmt` rewrites formatting.
- `just lint` validates the configuration and checks both code and formatting without rewriting files.

No separate formatter installations or deprecated v1 configuration are needed.

## Vulnerabilities and runtime checks

Run `govulncheck` separately from source-pattern analysis. It consults the Go vulnerability database and uses call analysis to report known vulnerabilities affecting reachable code, including the standard library. It is not a general security proof.

`just vuln` is network-dependent and intentionally separate from the fast local `just check`. CI runs it on pull requests, main pushes, manual dispatch, and weekly so new advisories are checked even without code changes. Lookup or scanner failures fail the job rather than silently skipping the scan.

`just test` runs shuffled, race-enabled tests. Race detection is dynamic, not static analysis, and only covers executed paths. The CI matrix exercises Linux, macOS, and Windows. `just smoke` checks the built executable from outside a workspace with a nonexistent Docker socket, including its usage-error exit status.

`just bench` records the informational command handler's time and allocations. This is an in-process microbenchmark, not a process-startup or Docker orchestration latency budget. Representative end-to-end measurements remain necessary as described in the architecture.

## Installation and dependency isolation

Use golangci-lint's official prebuilt release, not `go install` or a `tool` directive. Its maintainers explicitly warn that building it in another module's dependency graph can change its tested dependency versions and omit replacement directives.

The recipe downloads the installer from the pinned release tag, verifies its committed SHA-256 before executing it, and lets that installer verify the release archive against the upstream checksums. The binary lives in an ignored, version-specific directory under `.tools/`.

Install govulncheck with `go install ...@v1.9.0` into its own version-specific `.tools/` directory. This uses its module graph and does not add development-tool dependencies to Trocker's `go.mod`. Go's module checksum verification remains enabled.

Tool installation and Go toolchain downloads happen during development setup, never when the built Trocker executable starts. Subsequent recipes reuse the installed binaries and Go caches. The setup does not replace globally installed Go or linter binaries.

GitHub Actions references are pinned to commit SHAs. The workflow invokes the same `just` recipes as local development rather than maintaining a second set of checks.

## Updating

1. Verify stable Go, golangci-lint, and govulncheck releases against their official sources.
2. Update the relevant pins. For golangci-lint, also verify and update the installer checksum.
3. Confirm the linter supports the selected Go language version and was built with a compatible Go version.
4. Run `just tools`, `just check`, and `just vuln`; review new diagnostics instead of automatically suppressing them.
5. Let the OS matrix validate platform behavior. Update this report when the baseline or policy changes.

## Sources

- [Go release metadata](https://go.dev/dl/?mode=json) and [release history](https://go.dev/doc/devel/release).
- [golangci-lint v2.15.0](https://github.com/golangci/golangci-lint/releases/tag/v2.15.0).
- [Recommended binary installation and source-install caveats](https://golangci-lint.run/docs/welcome/install/local/).
- [Default linters and formatting](https://golangci-lint.run/docs/welcome/quick-start/).
- [v2 configuration](https://golangci-lint.run/docs/configuration/file/) and [analyzer settings](https://golangci-lint.run/docs/linters/configuration/).
- [Formatter settings](https://golangci-lint.run/docs/formatters/configuration/) and [CLI behavior](https://golangci-lint.run/docs/configuration/cli/).
- [Go vulnerability management](https://go.dev/doc/security/vuln/) and [govulncheck module version](https://proxy.golang.org/golang.org/x/vuln/@v/v1.9.0.info).
