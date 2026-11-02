# Implementation plan and validation

This plan turns [the architecture](architecture.md) into incremental, testable slices. The current implementation includes configuration validation, Git workspace discovery, persistent registration, local Docker target resolution, and interactive `init`. Read-only container status/endpoints, pure planning/doctor, and initial Compose-backed `up` are implemented. Existing-resource start/stop/down operations are implemented; native foreground execution and optional native generated files are implemented on Linux/macOS.

Implement working workflows rather than completing packages in isolation. Introduce packages only when their responsibilities are needed. Inspect the official Docker and Compose SDK APIs before implementing integration behavior; delegate supported functionality rather than recreating it.

## Validation conventions

For every slice:

- Run `just check`: module consistency, lint/format checks, shuffled race tests, build, and executable smoke checks.
- Add focused tests for meaningful behavior, failure paths, and the slice's safety guarantees.
- Run `just vuln` when dependencies change; retain scheduled CI scanning.
- Keep help, version, and completion Docker-free and workspace-independent.
- Preserve existing CLI validation and output-error handling tests.

As integration coverage becomes necessary, add a simple `just integration` recipe running `go test -tags=integration ./...`. Keep Docker-dependent tests behind that build tag. In an explicitly requested integration run, an unavailable Docker daemon should fail with a clear diagnostic, not silently skip the suite. The normal `just check` remains Docker-independent.

Use real, disposable Docker resources to validate SDK integration and lifecycle semantics. Use narrow fakes or failure-injection seams only for failures that are otherwise impractical to reproduce. Do not mirror the Compose SDK behind a large mock interface.

Integration fixtures must use unique project names and test ownership labels, enforce timeouts, and clean up only verified fixture resources. Never run global prune commands. Use temporary directories and isolated state locations; never write tests against a developer's real registry. Use separate OS processes for locking and crash tests, because goroutines alone do not establish cross-process correctness.

Run platform-sensitive tests on Linux, macOS, and Windows. Docker integration CI can initially run on Linux; document the daemon and container-platform coverage rather than implying that a Go OS matrix validates every Docker environment.

## 1. Settle the first implementation contracts

**Contract baseline recorded:** [Initial implementation contracts](initial-contracts.md), with [manifest acceptance fixtures](fixtures/contracts/README.md). These define intended behavior; loader tests and runtime implementation follow in later slices.

### Implement or specify

- Define the minimal versioned `trocker.yaml` schema: Compose file references, explicit endpoints, and environment bindings.
- Define required endpoint fields, supported schemes/protocols, unknown-field handling, duplicate-key behavior, and manifest-relative path rules.
- Define the restricted binding syntax and how native versus container consumers select address representations. Expressions must not execute code or shell commands.
- Define registration prerequisites, repeated `init`, worktree moves, missing metadata, and explicit recovery behavior.
- Define how Docker configuration selects a target, how its durable identity is established, and which local/remote configurations the first slice supports.
- Define what `plan` does for an unregistered workspace without silently registering it.
- Define initial JSON output schemas, unavailable/unverified endpoint representations, secret-redaction rules, and command error categories. Preserve existing usage-error exit status 2.
- State initial isolation limitations explicitly. Defer generated files, detached native execution, gateways, and unsupported host-endpoint cycles.

### Validate

- Add valid and invalid manifest examples with expected outcomes; turn them into executable fixtures in step 2.
- Record acceptance cases for registration, target selection, read-only previews, and unsupported configurations.
- Review examples across README and architecture documentation for consistency. Mark proposed syntax as implemented only when it actually works.

### Completion gate

The first configuration and identity slice can be implemented without inventing externally visible behavior in individual command handlers.

## 2. Load and validate configuration

**Internal loader implemented:** tagged Go structs and strict YAML library decoding, separate semantic/reference validation, and the official SDK's offline `LoadProject`. Contract fixtures and SDK loading tests run in `just check` without Docker. CLI workflow wiring and workspace discovery remain deferred.

### Implement

- Add `internal/config` for Trocker manifest decoding, structural validation, and path resolution.
- Add a thin `internal/compose` integration using compatible pinned Compose, Docker CLI, and Docker client dependencies.
- Delegate Compose loading, interpolation, includes, profiles, merging, and Compose-specific validation to the SDK.
- Load the Compose project once per configuration attempt. Validate Trocker service, endpoint, and binding references against that model.
- Return actionable errors identifying the manifest field or reference without exposing secret values.
- Keep loading separate from resource mutation and lazily initialize daemon access only when required.
- Expose this through internal workflow tests first; do not add a new public command solely to exercise a loader.

### Tests

- Minimal valid manifest and representative multi-service configurations load.
- Missing fields, invalid types, unsupported versions, duplicate keys, unknown fields, and malformed expressions follow the agreed contract.
- Missing services, invalid ports, unsupported schemes/protocols, and unresolved references fail clearly.
- Manifest-relative paths work from another current directory, including paths with spaces.
- Representative interpolation, multiple Compose files, includes, and profiles exercise SDK delegation. Do not retest the SDK's entire parser.
- Input manifests and Compose files remain unchanged after success and failure.
- Loader-only operations work with an unavailable Docker socket where daemon access is unnecessary.
- Errors and diagnostics omit supplied secret values.

### Completion gate

Explicit configuration produces a validated project and binding inputs without custom Compose parsing or Docker mutation. Run `just check` and `just vuln`.

## 3. Establish workspace identity and persistent state

**Implemented baseline:** persistent UUIDs and per-worktree Git identity markers, confirmed registration/linked-move association, private versioned registry with atomic replacement and process locks, SDK-based local daemon identification, and interactive `init`. Initialization validates existing manifests or proposes a minimal SDK-discovered file list; endpoint/binding inference is deliberately deferred. Tests cover separate-process contention, killed writers, identity recovery errors, and real-daemon confirmation/target resolution. Runtime ownership checks and resource queries follow in step 4.

### Implement

- Add `internal/workspace` for Git checkout/worktree identification and registration association rules.
- Add `internal/state` for platform-specific state locations, versioned JSON, atomic replacement, private permissions, and cross-process locks.
- Persist canonical workspace UUIDs, workspace associations, and required target associations. Do not persist a Docker inventory or resolved secrets.
- Resolve Docker configuration and target identity through the integration; ensure all SDK operations use the same selected target.
- Define lock order and introduce workspace/target and short-lived registry lock scopes as needed.
- Implement the minimal `init` workflow: show explicit proposals, obtain confirmation before writing, register once, and avoid overwriting existing configuration without consent. Limit discovery to concrete supported suggestions.
- Reject corrupt, unsupported, missing-but-ambiguous, or conflicting metadata instead of silently resetting or adopting resources.

### Tests

- Repeated registration, branch switches, and supported worktree moves preserve UUIDs.
- Independent clones and linked worktrees receive distinct identities.
- Ambiguous moves or missing associations fail according to the recovery contract.
- A declined initialization proposal writes neither configuration nor registration.
- Existing configuration is not silently overwritten.
- Platform state-directory selection and private permissions meet the documented contract.
- Concurrent subprocess registrations do not lose updates or create duplicate registrations.
- Interrupted atomic writes leave a valid previous or new registry, not truncated JSON.
- Locks release after normal exit and process death; cancellation while waiting does not corrupt state.
- Corrupt or unsupported state remains untouched after an error.
- Target aliases resolving to the same daemon follow the identity contract; unreachable targets do not fall back to another daemon.

### Completion gate

A workspace has a durable identity and target association that survive retries and process failure. Run `just check`; run target-resolution integration tests where applicable.

## 4. Implement observational commands

**Container-observation baseline implemented:** `status`, `endpoints`, and `endpoints --json` use Compose's project-scoped `Ps` with the already resolved target. Ownership discrepancies fail without adoption. Tests cover real running/unhealthy/stopped containers, missing services, project isolation, conflicting ownership, CLI JSON, and unchanged local files. Retained network/volume inventory is not yet implemented; this is not a complete resource inventory. Integration tests pull `busybox:1.37`, create uniquely labeled disposable containers, and verify fixture ownership before cleanup.

### Implement

- Add focused `internal/lifecycle` query workflows rather than a universal startup pipeline.
- Use Compose project-scoped queries for resources, service membership, health, and published ports.
- Add targeted Docker calls only for concrete information not exposed by Compose, reusing the resolved target configuration.
- Implement `status` and `endpoints`, including JSON output and explicit unavailable/unverified states.
- Surface missing or conflicting ownership without adopting resources.
- Keep observations distinct from preferences: a published mapping is not by itself proof of application readiness.
- Query only the selected workspace; do not scan every registered stack or mutate registry metadata.

### Tests

- Real Docker fixtures cover running, stopped, unhealthy, and missing services.
- Queries select only the intended project/workspace and report ownership discrepancies.
- Endpoint output reflects actual mappings rather than stale registry preferences.
- Missing mappings and remote publishing-host behavior follow the supported-target contract.
- JSON parses against the agreed shape and contains no progress text or secrets.
- Unreachable targets return actionable failures without fallback.
- Before/after comparisons show no resource, registry, configuration, or claim mutations.
- Unregistered-workspace queries follow the specified read-only behavior.

### Completion gate

Users can inspect registered stacks without reconciliation or side effects. Run `just check` and `just integration`.

## 5. Implement pure planning and diagnostics

**Initial subset implemented:** SDK-deep-copied project transformation, stable managed names/build tags, identity injection, container bindings, loopback Docker-assigned publications, and read-only `plan`/`doctor`. The loader uses compose-go's pre-normalization unsupported-attribute API to reject explicit names without re-parsing YAML. Host container bindings now use the step-8 allocator; unsafe sharing still fails before mutation; additional narrow limitations are documented in the contracts/README. Plan output omits environment values.

### Implement

- Add `internal/plan` with ordinary input/output types and pure functions.
- Compute stable project/resource names, ownership labels, endpoint representations, environment bindings, and isolation transformations.
- Replace host publications rather than appending conflicting mappings; preserve container ports and protocols.
- Define treatment of explicit container names, owned image tags, external names, writable shared mounts, and unsupported network modes.
- Inject the registered `TROCKER_WORKSPACE_ID` after supplied configuration and reject conflicting explicit bindings.
- Distinguish proposed assignments from reservations and runtime observations.
- Implement `plan` and expand `doctor` using the same validation and planning decisions that `up` will consume.
- Report unsupported binding dependencies, required consumer restarts/rebuilds, and unsafe sharing without performing repairs.

### Tests

- Equivalent inputs yield equivalent plans; input models are not mutated.
- Separate workspaces receive isolated names and ownership metadata.
- Container and host representations resolve correctly, including URL formatting and supported IPv6 addresses.
- Publication transformations preserve container ports/protocols and do not duplicate mappings.
- Identity injection and binding precedence follow the contract; conflicting values fail.
- External resources, explicit names, shared writable mounts, and host networking are accepted only under defined policies.
- Unresolved references, unreachable network relationships, and unsupported cyclic dependencies fail before execution.
- Plans redact secrets while retaining useful diagnostics.
- CLI preview and doctor tests prove there are no file writes, claims, registration, or Docker mutations.

### Completion gate

A deterministic plan explains the supported transformations and rejects unsafe configurations without effects. Run `just check` and relevant observation integration tests.

## 6. Deliver the first working `up`

**Initial subset implemented:** serialized workspace mutation; preflight and post-start ownership checks for project containers, named networks/volumes, and built image tags; Compose SDK `Up`, service/dependency selection, build flag, detached readiness, and foreground logs. No orphan removal or automatic destructive prompts. Real-Docker tests cover reachable endpoints, unchanged retries, recreation with volume-data preservation, independent/concurrent stacks, builds, selection, foreground completion, foreign retained resources, cancellation recovery, and build failure. Readiness currently uses a two-minute SDK timeout. Cross-platform runtime validation still relies on CI; the local runtime checks use Docker Desktop/Linux containers.

### Implement

- Support a deliberately narrow configuration whose endpoint bindings can be resolved safely. Reject unsupported dependencies explicitly.
- Coordinate workspace locking, configuration loading, observations, ownership validation, planning, and in-memory Compose transformation.
- Prefer Docker-assigned host ports when consumers can wait for discovery. Do not build a free-port picker for this case.
- Execute through Compose lifecycle APIs, preserving SDK build/dependency behavior and progress facilities.
- Propagate cancellation through SDK calls. Keep progress separate from machine-readable results.
- Observe actual mappings and configured readiness before reporting verified outcomes; distinguish mapping verification from application readiness.
- Persist only necessary confirmed preferences. On failure, preserve identity and recoverable resources rather than deleting data as rollback.
- Reinspect after interruption and revalidate ownership at mutation boundaries.

### Tests

- A disposable stack starts with correct workspace identity, labels, and reachable endpoints.
- A repeated unchanged `up` does not unnecessarily recreate containers or trigger builds.
- Independent workspaces start concurrently without name or port collisions.
- Conflicting existing ownership prevents adoption or mutation.
- Supported service selection and build/detach flags match their documented behavior.
- Configuration, build, start, and readiness failures are distinguishable and actionable.
- Cancellation and subprocess termination during startup leave state that a subsequent invocation can inspect and recover.
- Recovery after partial creation succeeds without destroying persistent data.
- Failed startup does not report proposed or stale endpoints as verified successes.
- Record cold/warm and no-change latency alongside equivalent Compose behavior.

### Completion gate

A supported stack starts, remains isolated, and recovers from partial failure. Run `just check`, `just integration`, and initial end-to-end measurements.

## 7. Complete lifecycle operations

**Implemented:** SDK start/stop/down, service selection, explicit volume removal, native Compose rendering, and unchanged registration. Lifecycle operations use live-resource reconstruction rather than startup planning or current configuration files. Tests cover volume-data reuse after down/up, attached anonymous deletion, external volume/bind/image retention, foreign-volume preflight, fixed-port conflicts, service selection, repeated teardown, and cancellation/retry. Actual Docker testing established that dynamically allocated host ports may change on start even though container IDs and HostConfig remain unchanged; step 8 now coordinates ports needed by pre-start bindings, while other publications remain Docker-assigned.

### Implement

- Implement `start`, `stop`, `down`, then explicit `down --volumes` through corresponding SDK operations.
- Use only each command's necessary inputs and safeguards; do not route everything through startup planning.
- Verify project membership and Trocker ownership before mutations.
- Preserve UUIDs and registry associations across every teardown mode.
- Keep images, bind-mounted files, and external resources outside default deletion.
- Direct port conflicts or changed configuration on existing containers to `up`; `start` must not recreate containers or rewrite their configured bindings. Docker-assigned ephemeral port numbers may change.

### Tests

- `start` preserves container IDs and configured bindings; actual Docker-assigned host ports are observed afresh.
- `stop` preserves containers, networks, volumes, and identity.
- `down` removes managed containers/networks but retains named-volume data and identity.
- Subsequent `up` reconnects retained managed named volumes and reads a previously written sentinel value.
- Retained anonymous volumes are not promised automatic reattachment.
- `down --volumes` removes intended managed named and attached anonymous volumes only.
- External resources, unrelated projects, images, and bind-mounted files survive teardown.
- Conflicting ownership blocks destructive operations.
- Start-time port conflicts fail without silent reassignment.
- Repeated teardown, partial failure, and cancellation follow defined SDK-compatible behavior without broad cleanup.

### Completion gate

Lifecycle semantics and retention guarantees are demonstrated with real resources and data. Run `just check` and `just integration`.

## 8. Expand planned allocation and binding coordination

**Implemented for the supported local IPv4 loopback TCP scope:** host container bindings, alias deduplication, self-advertised URLs, compatible live/preference reuse, OS-selected candidates, cross-process per-port leases, bounded corroborated bind-conflict retries, fresh consumer configuration on retries, actual-mapping verification, and confirmed preference persistence. No manifest syntax changes. `plan` remains read-only. Claims use OS lock ownership rather than registry PID records, so process death releases them without stale-claim repair; registry locks never span SDK operations. Other ports remain Docker-assigned.

**Validated:** real HTTP-advertised URLs, unchanged-up stability, down/up preference and data reuse, external listeners occupying preferences, a competitor stealing a port between create and start, retry exhaustion/recovery, cancellation, non-bind build failures, subprocess crash release, concurrent startups across repositories sharing state, and loopback/wildcard socket overlaps. A held pre-bind startup does not block another repository's metadata update or startup. General UDP/IPv6/non-loopback allocation, native claims, and representative performance budgets remain deferred; existing planner restrictions still apply.

### Implement

- Add planned ports only for concrete consumers requiring addresses before startup.
- Model allocation scope using publishing host/target, bind address, protocol, address family, and wildcard overlap.
- Reuse compatible preferences, record pending claims under a short registry lock, and let actual binds establish availability.
- Associate claims with sufficient operation/process identity for safe recovery; PID alone is insufficient.
- Release the shared lock before builds, pulls, readiness waits, or child-process lifetimes.
- Add bounded retries only for genuine bind conflicts, refreshing observations and replanning affected bindings.
- Reconfigure affected consumers before reporting success. Reject unsupported dependency cycles before launching consumers with invalid bindings.

### Tests

- Concurrent subprocesses across workspaces and repositories do not select conflicting planned bindings.
- Candidate selection covers supported TCP/UDP, IPv4/IPv6, specific-address, and wildcard overlap cases.
- An unrelated listener taking a candidate between selection and startup triggers bounded recovery, never termination of that listener.
- Non-bind failures are not retried as port conflicts.
- Changed assignments reach affected consumers; stale addresses are not reported as successful outcomes.
- Crash/restart tests recover abandoned claims using runtime evidence and preserve live claims.
- Slow operations in one workspace do not retain the shared allocation lock or block unrelated startups unnecessarily.
- Retry exhaustion and cancellation leave actionable, recoverable state.

### Completion gate

Pre-start address requirements work under contention without claiming socket reservations or replacing Compose orchestration. Run `just check`, `just integration`, and contention measurements.

## 9. Add foreground native execution

**Implemented on Linux/macOS:** `run SERVICE -- COMMAND`, selected native bindings, observed dependency addresses, authoritative identity, inherited environment with explicit overrides, exact argv/current directory, connected streams and terminal descriptors, child exit propagation, signal forwarding, bounded cancellation, and owned-process-group cleanup before leader reaping. No Docker mutations or lifetime workspace/registry locks. Already-published dependencies require no new allocation claims; native listening-port allocation is not implemented.

**Validated:** helper-process arguments/environment/streams and exit 2, offline literal/identity bindings, no container environment inheritance, missing executable/permission failures, cancellation and resistant descendants, normal-exit descendant cleanup, unrelated-listener preservation, real Docker HTTP dependency use and stopped/missing dependency refusal, unchanged files/resources, and lock availability during a native child's lifetime. Local real-terminal checks cover stdin, terminal detection, Ctrl+C, foreground restoration, SIGTERM forwarding, and executable exit status. Linux execution is tested in a disposable container in addition to CI. Windows builds remain supported, but native execution there fails explicitly; Windows process-tree support is deferred.

### Implement

- Add `internal/native` for foreground process execution only.
- Have lifecycle workflows prepare explicit native bindings and coordinate any necessary claims.
- Launch the child with inherited environment plus resolved bindings and authoritative workspace identity.
- Pass arguments directly without shell reinterpretation; preserve the CLI's `--` separator semantics.
- Connect standard streams, forward supported signals, wait for completion, and propagate exit status.
- Define platform-specific cancellation/process-tree behavior and distinguish launch failures from child exit codes.
- Maintain claims for the owned child's lifetime without holding the shared registry lock.

### Tests

Use a small test helper executable rather than relying on shell-specific commands:

- Child receives the intended environment, binding precedence, and workspace UUID.
- Arguments with spaces, empty values, flags, and additional separators arrive unchanged.
- Standard input/output/error behave correctly.
- Successful and nonzero child exits propagate, including a child exit code of 2 without treating it as a CLI validation error.
- Missing executable and permission failures produce useful errors.
- Cancellation and signals reach the child according to the platform contract; supported descendants do not leak.
- Claims remain valid while the child runs and are released or recoverable afterward.
- Unrelated listeners and processes are never signalled.

### Completion gate

`run` is a reliable foreground native-process command, not a supervisor. Run `just check`, platform subprocess tests, and integration tests for bindings/claims.

## 10. Add optional generated application files

**Implemented 10a:** explicit `native.files`, confined manifest-relative templates, standard `text/template` plus `json`, bounded rendering/JSON validation, private immutable generations, unchanged-output reuse, and explicit environment-path delivery before native launch. Literal/UUID-only files work offline. `init`/`plan`/`doctor` check inputs and syntax without rendering; `up` does not render native files. The demo worker's `examples/native-config` client reads its generated JSON and calls the owned API.

**Validated:** escaping and references, invalid templates/JSON/missing keys/size limits, path confinement and private permissions, subdirectory invocation and spaces in state paths, complete-set write/rename failures, concurrent publication, SIGKILL/retry, target/workspace/service separation, retained immutable snapshots, no lifetime consumer/workspace/registry lock, and a real Docker HTTP consumer with missing/stopped dependency refusal.

**Implemented 10b on Linux/macOS hosts:** explicit container `files` and targets, same-scope environment paths, selection/network/mount checks, file-only host-port allocation, complete-set rendering before startup, rerendering on bind retries, and SDK file-backed read-only configs. Container files are `0444` within private `0700` host directories, supporting arbitrary non-root UIDs without exposing the host tree. Opaque generation IDs in config references trigger SDK recreation on changed bytes; unchanged bytes reuse the existing container. The root demo worker consumes a generated API-address file.

**Validated 10b:** non-root BusyBox JSON consumption with a read-only root filesystem, inspected read-only mounts, private host directory permissions, unchanged-up stability, edited-template recreation, `start` retaining the previous generation, invalid-render refusal, `down --volumes` retention, and actual file content after a competing owned fixture steals the first allocated port.

**Remaining:** [the generated-file contract](generated-files-proposal.md) documents the supported SDK/permission policy. Windows-host file delivery needs ACL validation. No generated-file garbage collection is provided: parent death or an absent lock does not prove a surviving native child released its files, and stopped containers can retain mounts.

### Implement

- Add rendering helpers in lifecycle code only when a concrete application requires file-based configuration.
- Define explicit template inputs, outputs, and consumer mounts/arguments; do not implicitly rewrite tracked application files.
- Resolve inputs relative to the manifest and outputs beneath the private workspace/target generated directory.
- Reject traversal and symlink escapes, protect secret-bearing outputs, and replace files safely.
- Avoid rewriting unchanged content and avoid persisting resolved secrets in registry metadata.
- Reject unsupported remote bind mounts rather than assuming a local path exists on the daemon host.
- Remove outputs only when no managed consumer needs them; invalidate them when identity is explicitly forgotten.

### Tests

- Rendering produces expected content and explicit consumer paths.
- Input path resolution is independent of working directory.
- Absolute output paths, traversal, and symlink escape attempts fail without outside writes.
- Failed or interrupted writes do not leave partially rendered content for consumers.
- Secret-bearing permissions satisfy supported-platform requirements; diagnostics omit secret values.
- Unchanged rendering preserves files without unnecessary rewrites or consumer recreation.
- Workspace and target output directories remain isolated.
- Unsupported remote mounts fail before launch.
- Cleanup preserves files needed by existing consumers and never deletes source files.

### Completion gate

Generated files remain optional, private, disposable configuration—not a second source of truth. Run `just check`, filesystem tests on supported OSes, and consumer integration tests.

## Cross-cutting release gates

- Keep documentation explicit about implemented versus planned behavior and supported targets/platforms.
- Run the full unit/race, executable smoke, integration, and vulnerability suites.
- Exercise registration → plan → up → status/endpoints → stop/start → down → up → down --volumes using real data.
- Exercise interruption and retry at state writes, startup, allocation, and teardown boundaries.
- Confirm ownership checks, read-only query behavior, cancellation, and secret redaction across command boundaries.
- Measure cold/warm commands, no-change startup, many registered workspaces, concurrent startup, and lock contention. Establish numerical budgets from representative measurements, not guesses.
- Compare coordination overhead with equivalent Compose SDK operations; avoid attributing all SDK initialization/loading cost to unavoidable daemon wait.

A slice is complete when its observable behavior, failure handling, ownership/non-mutation guarantees, and applicable concurrency properties are tested—not merely when it compiles or its mocked calls match expectations.
