# Development guidelines

- Write the simplest, clearest code that solves the current problem. No overengineering.
- Prefer idiomatic Go and standard-library functionality over custom machinery.
- Delegate everything the Docker and Compose SDKs support to those SDKs. Check their APIs before implementing custom logic; do not duplicate their functionality.
- Add abstractions, dependencies, configuration, or infrastructure only for a concrete present need—not hypothetical future requirements.
- Keep functions focused, names descriptive, and control flow straightforward.
- Workflow functions should read as a short sequence of named steps (e.g. prepare, confirm, apply), not mix discovery, validation, prompting, locking, and persistence inline. Extract cohesive helpers before the workflow becomes hard to follow; keep lock scopes, cleanup, and mutation boundaries explicit. Avoid trivial wrappers or abstractions added only to shorten functions.
- Keep tooling simple: task recipes run commands, not custom package managers or test frameworks.
- Comment on non-obvious constraints and reasons, not what the code already says.
- Keep changes scoped, preserve unrelated work, and run the relevant checks before handoff.
- Keep this file lean. Put architecture and detailed workflows in `docs/`.

## CLI output

- For commands that mirror Compose (`up`, `start`, `stop`, `down`), reuse Compose's own renderer through its SDK progress events. Do not recreate its spinners, resource rows, timings, or status formatting. Use its terminal renderer interactively and plain-text renderer for pipes/CI. Add only necessary Trocker context before or after the display, never interleaved with live progress; retain Trocker's ownership and retention safeguards.
- Use concise, scannable output: a short `error:` summary, then optional indented `help:` (action) and `note:` (context), separated from the summary by a blank line. Avoid paragraphs, stacked package prefixes, log timestamps, and decorative boxes.
- Keep diagnostic meaning separate from rendering: internal errors provide safe summaries and optional help/context; `internal/cli` owns presentation. Classify known SDK failures rather than hiding every cause or exposing raw secret-bearing errors.
- Show initialization as a compact labeled summary, an explicit confirmation, and a brief result stating what changed. Keep proposed file changes visible; do not print implementation chatter.
- Send diagnostics, progress, and prompts to stderr; command results and machine-readable data go to stdout. Never decorate JSON or piped output.
- Use color only for emphasis, never as the sole indicator. Check the destination stream's terminal status; honor `NO_COLOR` and `TERM=dumb`. Plain text must remain fully readable.
- Follow [CLI Guidelines](https://clig.dev/) and [Rust's diagnostic conventions](https://rustc-dev-guide.rust-lang.org/diagnostics.html#diagnostic-output-style-guide); prefer consistency over custom UI machinery.

## Tests

- Put tests for a Go file in a sibling file named after it: `containers.go` pairs with `containers_test.go`, or `containers_integration_test.go` behind a build tag. Do not add test files that do not mirror the name of an implementation file.
- Test Trocker's decisions and contracts: validation, transformations, ownership, retention, and failure recovery. Each test must catch a plausible bug or protect a requirement—not raise coverage for its own sake.
- Test pure rules directly and SDK integration with real disposable resources. Do not retest library internals, mirror implementation steps, or mock SDK calls merely to verify call sequences.
- Explicitly test safety guarantees: read-only commands do not mutate state, teardown preserves unowned resources, and errors do not expose secrets. Never use a developer's existing Docker resources as test fixtures.
