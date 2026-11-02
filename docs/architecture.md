# Trocker architecture

Trocker is a daemonless Go CLI that runs isolated Docker Compose stacks for Git worktrees. It supplies the workspace identity and application-specific configuration that Compose does not know about, while delegating container management to the official Docker and Compose SDKs.

This is the main architecture reference. The [endpoint design report](reports/worktree-isolation-and-endpoint-bindings.md) and [state design report](reports/go-architecture-and-state.md) provide additional rationale.

**Implementation status:** manifest validation, Git discovery/association, offline Compose loading, local Docker target resolution, persistent registration, and minimal interactive `init` exist. `init` validates or proposes configuration and registers identity without creating Docker resources. Read-only container `status` and `endpoints` work, including endpoint JSON; retained networks/volumes are not yet inventoried. Pure planning, `doctor`, and an initial Compose-backed `up` also work. Help, version, and completion work; `start`, `stop`, and `down` operate on verified existing resources through Compose's native renderer. Native `run` executes local foreground commands on Linux/macOS and optionally publishes explicit private generated configuration files. The components and workflows below describe the intended implementation, not functionality already available. Manifest examples below are conceptual syntax. The [initial implementation contracts](initial-contracts.md) define the versioned schema, supported first-slice subset, and acceptance cases; the broader examples in this document and design reports do not override that contract.

## Design principles

- **Delegate to the SDKs.** Check the Docker and Compose APIs before implementing custom behavior. Do not duplicate project loading, container discovery, dependency ordering, lifecycle operations, or other functionality they already provide.
- **Keep it simple.** Prefer concrete types and ordinary functions. Add packages and interfaces when their responsibilities exist, not to populate an architectural diagram.
- **Explicit configuration at runtime.** Discovery helps users author configuration during setup; normal commands do not guess what application settings mean.
- **Separate decisions from effects.** Planning computes desired changes. Lifecycle code coordinates I/O and execution.
- **Preserve data and ownership.** A matching name or port is not permission to adopt or delete a resource.
- **Stay responsive.** Do only the work required by the selected command and workspace. Trocker must not become a bottleneck ahead of Docker.

## What Trocker owns

| Owner | Responsibility |
| --- | --- |
| Trocker | Worktree identity, workspace-to-project association, isolation policy, explicit endpoint bindings, allocation coordination, and native-process configuration. |
| Compose SDK | Compose loading and semantics, project resource discovery, builds, dependency orchestration, lifecycle operations, and runtime queries. |
| Docker | Actual containers, networks, volumes, published ports, and runtime state. |
| Application | The meaning of its configuration, external-service partitioning, cloud-resource provisioning, permissions, and cleanup. |

A workspace gets a stable, unique Compose project name derived from its persisted identity. Compose already labels resources and knows which containers belong to that project across repeated invocations. Trocker does not build another container-discovery system.

## Components

```text
cmd/trocker/          Process entry point
internal/
    cli/             Command-line interface and presentation
    config/          Trocker manifest loading and validation
    workspace/       Git worktree identification and identity rules
    state/           Registry persistence and process locks
    plan/            Pure isolation and binding decisions
    compose/         Compose SDK operations and runtime observations
    lifecycle/       Application workflows and effect coordination
    native/          Foreground child-process execution
    generated/       Confined templates and bounded native file rendering
```

`cmd/trocker`, `internal/cli`, `internal/config`, loading/target resolution in `internal/compose`, `internal/workspace`, `internal/state`, and initialization in `internal/lifecycle` currently exist. The remaining directories are responsibility boundaries to introduce as implementation requires them.

### `cmd/trocker`: process entry point

Connect process arguments and standard streams to the CLI, report errors, and choose the exit status. Keep business logic out of `main`.

### `cli`: user interface

Own Cobra commands, flags, argument validation, help, completion, version output, progress presentation, and human-readable or JSON results. Command handlers call application workflows rather than manipulating Docker resources or implementing isolation rules.

Keep Cobra types inside this boundary. Construct a fresh command tree per invocation, with explicit output streams. Help, version, and static completion generation must not initialize Docker clients or read workspace state.

### `config`: explicit application configuration

Read `trocker.yaml` and validate its structure: Compose file references, endpoint definitions, bindings, and optional generated-file declarations. Resolve manifest-relative paths and report actionable configuration errors.

Delegate parsing, includes, interpolation, profiles, and Compose-specific validation to the Compose SDK. Once the project is loaded, validate Trocker references against its services, networks, and ports. Do not infer missing endpoint fields at runtime.

Discovery for `init` can inspect Compose metadata and selected application settings to suggest configuration. Suggestions require confirmation and must not silently rewrite application files.

### `workspace`: identity rules

Identify the current Git checkout/worktree and associate it with its persisted workspace UUID. Distinguish separate clones and worktrees, preserve identity across branch changes and supported moves, and reject ambiguous associations.

Git discovery uses focused `Discover` and `List` functions backed by `os/exec.CommandContext`, not a shell or an alternate Git implementation. `Discover` returns canonical checkout, per-worktree Git, and common Git directory paths; `List` uses NUL-delimited porcelain output and preserves locked/prunable records without cleanup. Repository-local Git environment overrides are removed for these subprocesses so they inspect the selected directory; normal global/system configuration and Git ownership checks remain in effect. Git must be installed. Registration matches these paths to a persistent UUID. A matching UUID marker in the per-worktree Git directory detects lost registry associations and unsupported moves of the entire Git directory.

This component decides which workspace an invocation refers to; `state` handles how that association is stored. Neither a branch name, mutable path, port assignment, nor reusable slot is a workspace identity.

### `state`: durable metadata and locking

Store the minimal registry, perform atomic writes, protect metadata permissions, and provide cross-process locking. Persist identities, target associations, port preferences, and necessary pending allocation claims.

The initial store contains only versioned workspace registrations and daemon IDs. Initialization takes a workspace lock keyed by Git administrative path (across targets), then a short registry lock; prompts and Docker calls happen before either lock. Writes use the existing Moby atomic-writer library, and OS locks use `gofrs/flock`. Unix state permissions are private; Windows relies on the user profile's inherited ACL. Tests cover process interruption, not guarantees against filesystem/power-loss failures.

Do not store a duplicate Docker inventory, resolved secrets, or complete Compose projects. Corrupt or unsupported state is an error requiring recovery, not permission to reset identities or adopt existing resources.

### `plan`: decisions without side effects

Given validated configuration, the loaded project's relevant facts, workspace identity, preferences, and observations, compute:

- Workspace-specific project/resource names and ownership labels.
- Which resources are isolated, intentionally shared, or unsupported.
- Candidate port assignments or requests for Docker-assigned ports.
- Container and host endpoint representations.
- Resolved environment bindings and required project transformations.
- Configuration changes that require consumers to restart or rebuild.

Return a plan or useful validation errors. Do not call Docker, read files, launch processes, mutate the registry, or acquire locks. A proposed port is not a reservation, and a plan is not proof that its endpoints are reachable.

`plan` and `up` share these decisions. Keep binding resolution and port-selection policy as functions here; they do not initially need separate packages or a generic expression engine.

### `compose`: SDK integration and observations

Own the integration with the official Compose SDK: load a project, apply the planner's transformations in memory, validate the resulting model, execute lifecycle operations, and query actual resources.

Use Compose's project-scoped APIs first. For example, `Ps` exposes container service membership, state, health, labels, and published ports. Compose performs the underlying Docker inspection; Trocker should not duplicate it.

**There is no separate `engine` component.** If a concrete requirement is not exposed by Compose, such as additional retained-volume inspection, use a targeted Docker SDK call within this integration. Reuse the same resolved Docker configuration and target; do not create independently configured clients that might address different daemons.

Keep the integration thin. Use SDK types where useful rather than recreating the full Compose schema. Pin compatible Compose, Docker CLI, and Docker client versions together. Normal operations do not shell out to the Compose executable.

### `lifecycle`: coordinate the work

Implement application workflows for initialization, preview, queries, startup, teardown, and preparation for native execution. Each command gets the shortest workflow it needs; this is not one universal startup pipeline.

Coordinate configuration loading, workspace lookup, locks, observations, planning, allocation claims, optional generated files, SDK calls, verification, and metadata updates. Handle partial failures and genuine bind conflicts without reimplementing Compose's service orchestration.

Enforce Trocker's ownership and retention policy at mutation boundaries. Use the SDK's progress and readiness facilities, and propagate cancellation. The planner decides what should change; the coordinator arranges the effects.

Optional application-file rendering and writing can start as focused helpers here. Extract another package only if the implementation warrants it.

### `native`: run a local program

Execute a foreground process outside Docker with its resolved environment and `TROCKER_WORKSPACE_ID`. Connect standard input/output, forward signals, wait for completion, and propagate the child exit status.

For example, `trocker run web -- pnpm dev` runs a native frontend with bindings for the selected service. The separator keeps child flags out of Trocker's argument parser. This deliberately differs from Compose's `run`, which starts a one-off container.

The lifecycle coordinator loads the active service, resolves native endpoint references from both environment and file inputs using verified live Docker observations, publishes explicit generated files, and injects workspace identity. Literal/UUID-only bindings need no daemon. No claims are needed for consuming already-published Docker endpoints, and no registry/workspace lock spans the child lifetime. The native application's own listening-port allocation remains out of scope.

`native` delegates argv, environment deduplication, and stream copying to `os/exec`. Linux/macOS use a private process group, real terminal handoff, and SIGINT/SIGTERM forwarding with five-second cancellation escalation. To avoid signalling a reused group ID, the leader is observed exiting without reaping (Linux waitid/WNOWAIT, macOS kqueue), remaining group members are killed, and only then does `Cmd.Wait` reap it. Terminal ownership and settings are restored using OS and Moby terminal helpers. Child exit status is distinct from CLI usage/launch failures. Windows native execution is explicitly unsupported pending a tested process-tree implementation. Do not add detached supervision or infer ownership from a listening port or a persisted PID alone.

## Core concepts and sources of truth

| Concept | Meaning and authority |
| --- | --- |
| Workspace | A Git worktree associated with a persistent UUID in Trocker's registry. |
| Runtime target | The actual Docker daemon and publishing host, resolved through Docker configuration. A context name alone is not durable target identity. |
| Compose project | The workspace's stable container-management scope, identified by a unique project name. |
| Endpoint | An explicitly declared service, container port, and scheme, with addresses for different consumers. |
| Plan | Proposed transformations and actions derived from configuration and observations. Not runtime truth. |
| Observation | Actual state obtained through the SDKs. Authoritative for existing resources and mappings, but subject to change after inspection. |
| Port preference | A remembered assignment that may be reused if still valid. Not proof of availability or a live endpoint. |
| Generated file | Disposable application configuration derived from explicit inputs. Not authoritative state. |

### Workspace identity

Generate a random UUID once at registration and persist its lowercase canonical representation. A `trocker-workspace-id` file in the per-worktree Git administrative directory contains the same UUID; it is association evidence, not authority to adopt resources. Commit the registry before publishing this marker so a retry can finish initialization with the same UUID. A marker without a matching registry record is a recovery error, not permission to recreate state. Preserve it across repeated initialization, branch switches, supported worktree moves, rebuilds, port changes, `stop`, and every form of `down`.

Inject the registered value as `TROCKER_WORKSPACE_ID` into managed containers and native children, and expose it as `workspace.id` in bindings. Apply it after supplied secret-provider configuration and reject conflicting explicit bindings. The application-visible identity and Trocker's ownership metadata must agree.

Explicitly forgetting a workspace ends its identity's lifecycle; a replacement registration gets a new UUID. The exact recovery/forgetting interface is not yet implemented. Missing metadata must never silently authorize adopting existing resources.

The UUID is non-secret metadata, not an authentication credential. An application may use it to namespace a queue or object prefix, but remains responsible for consistent use, provider naming limits, and external-resource cleanup.

## Configuration and endpoint bindings

A proposed manifest:

```yaml
compose: compose.yaml

endpoints:
  api:
    service: backend
    port: 8000
    scheme: http

bindings:
  web:
    environment:
      INTERNAL_API_URL: "{{ endpoints.api.container.url }}"
      BROWSER_API_URL: "{{ endpoints.api.host.url }}"
```

`api` is an endpoint alias. `backend` and `web` are actual Compose service names. Every endpoint explicitly declares `service`, `port`, and `scheme`; the port is the container's listening port, not the published host port. Declaring HTTPS does not enable TLS.

| Address | Consumer | Example |
| --- | --- | --- |
| Container | A container sharing the appropriate network | `http://backend:8000` |
| Host | A native process, debugger, or host CLI | `http://localhost:24173` on a local target |

Choose the representation by where the value is ultimately consumed, not where the environment variable is stored. A frontend container may need both a container URL for server-side calls and a host URL advertised to the browser at runtime. Native callers use explicit host-address bindings; do not blindly substitute every container URL with localhost.

Expose individual address components such as host and port as well as URLs. Keep credentials separately supplied. Remote Docker targets must use their actual reachable publishing host rather than assuming localhost.

Bindings resolve a restricted set of explicit references, not arbitrary code or shell commands. Reject unresolved references and unsupported network reachability. Report mismatches with Compose port declarations without silently rewriting the manifest; declared ports alone do not prove that an application is listening.

Apply Trocker-generated bindings after supplied environment/secret inputs. Distinguish runtime environment from build-time configuration: changing runtime variables cannot repair a URL already compiled into an image. Start with environment bindings; add explicitly requested file templates, structured patches into generated copies, or build arguments only for concrete consumers.

## Isolation and resource ownership

Use Compose's existing project namespacing wherever it is sufficient. Trocker's transformations cover the gaps:

- Assign the stable workspace-specific project name and workspace ownership labels.
- Remove or namespace explicit container names and isolate owned build-image tags where necessary.
- Replace host-port publications instead of appending competing entries. Preserve container ports and transport protocols.
- Apply resolved bindings and explicitly configured generated-file mounts.
- Detect explicit or external network/volume names and shared writable bind mounts. Require a policy for intentional sharing rather than claiming those resources are isolated.
- Reject unsupported configurations such as host networking instead of pretending port remapping isolates them.

Check project membership and Trocker ownership before mutations. A recycled port, matching branch name, or familiar resource name is not ownership evidence. External networks, volumes, and application-owned cloud resources are never deleted by Trocker teardown.

## Command workflows

The following describes target behavior. Minimal `init`, read-only observations, planning/doctor, and initial startup are implemented; existing-resource lifecycle operations are also implemented; native foreground execution is implemented on Linux/macOS.

### `init`

Identify the workspace, load Compose metadata, and propose complete endpoint definitions and bindings for confirmation. Persist approved Trocker configuration and registration without changing arbitrary application settings. Repeated initialization preserves the UUID. Discovery stays in this explicit setup path, not ordinary runtime commands.

### `plan`

Load configuration, identity, relevant preferences, and current observations; run the pure planner and explain the proposed changes. Do not allocate claims, start containers, generate application files, or report proposed endpoints as verified.

Previewing an unregistered workspace needs a defined identity policy before implementation; it must not silently persist a new identity as a side effect of a read-only preview.

### `up`

```text
Validate workspace identity and acquire workspace/target lock
    → load explicit configuration and Compose project
    → observe existing project resources and verify ownership
    → plan changes and coordinate necessary allocation claims
    → transform the Compose model in memory
    → render application files only if explicitly configured
    → apply through the Compose SDK
    → observe actual mappings and configured readiness
    → persist confirmed preferences and release pending claims
    → report verified results
```

Refresh observations and claims before applying a previewed plan. Let Compose handle ordinary dependency ordering, builds, and container recreation. Stage work only where Trocker's endpoint bindings require runtime information before a consumer can be configured.

### Lifecycle and query commands

| Command | Required behavior |
| --- | --- |
| `start [SERVICE...]` | Start existing stopped containers without recreating them or rewriting configured publications/bindings. Docker-assigned host port numbers may change. Fixed-port conflicts fail; use `up` for configuration changes and `endpoints` for current mappings. |
| `stop [SERVICE...]` | Stop containers without removing them. Preserve networks, volumes, and workspace identity. |
| `down` | Remove project containers and managed networks; retain volumes and workspace identity. |
| `down --volumes` / `-v` | Also remove declared managed named volumes and attached anonymous volumes after ownership checks. Never remove external volumes. |
| `status` | Query the selected workspace's actual resources and state. Do not reconcile or mutate them. |
| `endpoints` | Report observed mappings and derived addresses, distinguishing unverified or unavailable endpoints. Never present stored preferences as live mappings. |
| `doctor` | Diagnose invalid references, unsafe sharing, ownership problems, and unsupported isolation. Do not repair or delete resources automatically. |
| `run SERVICE -- COMMAND [ARG...]` | Observe referenced native dependencies, render/publish explicit native files, resolve environment bindings, then execute the foreground child. No native listening-port allocation. |

`start`, `stop`, and `down` call corresponding SDK operations with only the inputs and safeguards they need; they do not rerun startup planning. Images and bind-mounted files are not deleted by default. Retained anonymous volumes are not automatically reattached after recreation; persistent data should use managed named volumes or explicit mounts.

`status`, `endpoints`, and `doctor` must not register workspaces, allocate ports, or perform cleanup. Worktree deletion or PR merge never authorizes destructive teardown.

## Port allocation and concurrency

**Implemented scope:** local-host TCP/IPv4 loopback container bindings. `internal/plan` identifies publications required before startup; `internal/lifecycle/ports.go` acquires short-lived per-port leases, applies addresses to a fresh project, and delegates convergence to Compose. Other publications remain Docker-assigned. Actual mappings are verified before preferences are committed under the short registry lock. `plan` never acquires leases or resolves speculative addresses as live results.

Pending claims use OS-backed file locks rather than persistent PID/heartbeat records. All local daemon identities share the same publishing-host lease namespace in the user's state directory. The OS releases locks when the owning process exits; stale files do not need adoption or deletion. A subprocess test holds one startup before binding while an unrelated repository completes startup, despite sharing a candidate preference. Existing sockets (including IPv4 wildcard overlap) are probed conservatively, but successful Docker binding is authoritative. Candidates use the OS ephemeral range; limits are 32 candidate probes per publication and three startup attempts. Supported Engine bind-error messages must be corroborated by owned-container failed-start state; unrelated or unknown failures are not retried.

Self-advertised and mutually advertised URLs can be prepared without ordering cycles because all required ports are planned before startup. Compose's dependency graph still controls readiness and rejects its own cycles. Scope expansion to UDP/IPv6, non-loopback addresses, and native processes remains future work.

Prefer Docker-assigned host ports when consumers can wait for discovery. For example, start a database, query its published port, then launch a native application with that address. Do not implement a free-port picker where Docker's allocator already meets the requirement.

Some applications require a host URL before startup, including their own URL. These need planned assignments: reuse compatible preferences, evaluate candidate bindings, record pending claims, configure consumers, and attempt startup. A free-port check and a registry claim do not reserve a socket against unrelated programs. Docker's or the native application's successful bind is authoritative.

Allocation must account for the publishing host and daemon, bind addresses and wildcard overlap, TCP versus UDP, IPv4/IPv6, and concurrent starts across repositories. Prefer loopback publication unless explicitly configured otherwise. Container-to-container service ports and DNS names remain stable.

Use two lock scopes:

- **Workspace/target lock:** serialize conflicting operations on one workspace stack.
- **Shared registry lock:** protect short metadata updates across workspaces. Per-port OS locks independently protect pending claims without blocking unrelated ports or holding this shared lock.

Use a consistent lock order. Do not hold the shared lock during builds, pulls, readiness waits, or a native child's lifetime. Keep native claims valid while the owned process is using them, without blocking unrelated workspace activity.

On a genuine bind conflict, refresh observations and replan affected assignments and bindings. Do not retry unrelated failures as port conflicts or kill the existing listener. A changed binding may require affected consumers to restart or rebuild. Reject unsupported cyclic host-endpoint requirements before leaving consumers configured against unverified mappings.

## Persistence and generated files

Keep user-level metadata shared across repositories:

| Platform | State directory |
| --- | --- |
| Linux | `$XDG_STATE_HOME/trocker`, falling back to `~/.local/state/trocker` |
| macOS | `~/Library/Application Support/trocker` |
| Windows | `%LOCALAPPDATA%\Trocker` |

```text
<state-directory>/
    registry.json
    locks/
    generated/
        <workspace-id>/
            <target-key-or-local>/native/<service-key>/
                current.json
                <generation-id>/<output-filename>
```

Start with versioned JSON and atomic replacement under a process lock. Persist only workspace associations, runtime-target associations, port preferences, and necessary pending claims. Do not introduce a database, durable resource inventory, or generic operation journal without a demonstrated need.

The registry is not runtime truth. Retained volumes and containers remain discoverable through the SDKs, including after interrupted operations. Target identity must refer to the actual daemon rather than only a renameable context. If the selected target cannot be resolved or reached, fail rather than falling back to another daemon.

No generated Compose override is required: the SDK accepts an in-memory project. Application files are optional and disposable. Resolve template inputs relative to the manifest and outputs under the private workspace/target generated directory. Reject traversal and symlink escapes, restrict permissions for secret-bearing files, and avoid rewriting unchanged contents.

**Implemented native delivery:** `internal/generated` loads confined regular templates through `os.Root`, supplies only explicit string values to `text/template` plus a `json` encoder, and validates the complete bounded output set. `internal/state/generated.go` serializes only comparison/publication with a consumer-specific OS lock, stages exclusive private files, and atomically selects a complete generation. `current.json` is a cache hint, never runtime truth; identical current bytes reuse their paths. Changed output gets a new immutable snapshot. Native environment bindings explicitly receive `files.<name>.path`; argv is never interpolated. Literal/identity-only inputs stay offline. No locks span native execution.

Native directories are `0700`, files/metadata `0600`. Failed preparation launches no child and preserves previous outputs. No automatic cleanup removes old or uncertain generations, including on `down --volumes`; parent death cannot prove a surviving child released its files. Old secrets can remain in private state. **Implemented container delivery:** `up` renders selected container templates with the current allocation on each attempt. It publishes in a separate container scope, then adds SDK file-backed configs and service references. Compose creates individual read-only bind mounts. References contain opaque generation IDs, so Compose's normal service hash/reconciliation recreates consumers after changed content without exposing content digests. `start` keeps existing mounts. Mount overlap and special-target checks happen before mutation.

Container files are `0444` inside the same private `0700` directory policy; metadata stays `0600`. Container UIDs can read mounted files without granting other ordinary host users directory traversal. This is not per-UID secrecy inside a container. Windows-host delivery is rejected until ACL semantics are validated; the daemon must be able to access the private host tree. All old or uncertain generations are retained. See the [generated-file contract](generated-files-proposal.md). No local file is assumed to exist on a remote daemon.

## Failure handling and safety

- Reinspect resources after partial startup or interruption. Do not assume nothing was created, silently adopt conflicting resources, or delete persistent volumes as rollback.
- Associate pending claims with an operation/process identity. A PID alone is insufficient for stale-claim recovery; inspect relevant ownership and runtime evidence before reuse.
- Treat locks as coordination between Trocker processes sharing the registry, not protection against every Docker client or host process. Revalidate at mutation boundaries.
- Preserve workspace identity through teardown and reject corrupt registry state instead of replacing it with an empty registry.
- Propagate cancellation through SDK operations and native execution. Leave recoverable state and actionable errors when an operation cannot finish.
- Redact secrets from plans, diagnostics, progress, and errors. Protect state and generated files with appropriate permissions.
- Do not infer external-service isolation. Applications own namespaced queues, buckets, databases, OAuth settings, and their cleanup.
- Different localhost ports do not isolate browser cookies. The first version does not promise browser-origin isolation or automatically adjust third-party allowlists.

## Performance and observability

Initialize dependencies only for commands that use them. Resolve the selected workspace rather than scanning every checkout or registered stack. Normal commands do not run discovery, contact Git hosting, invoke an LLM, check for updates, or perform automatic maintenance.

Load the Compose project once per configuration attempt. Reuse SDK clients and project-scoped queries. Keep transformations deterministic so an unchanged stack does not rebuild or recreate solely because Trocker ran. Preserve Docker/BuildKit cache behavior.

Use SDK events and readiness mechanisms rather than custom polling or sleeps. Stream progress without mixing it into machine-readable results. Include workspace, target, and operation context in diagnostics without exposing secrets.

Measure cold and warm command latency, no-change startup, concurrent workspaces, and lock contention. Separate Trocker's coordination overhead from builds and application waits, while still accounting for SDK initialization and loading costs. Set performance budgets and timeouts from measurements or documented upstream limits, not intuition. Do not add caches or background services before measurements justify them.

## Implementation sequence

1. Load and validate explicit configuration through `config` and the Compose SDK integration.
2. Implement workspace identity, atomic registry persistence, target resolution, and SDK observations.
3. Implement pure planning and observational commands before resource mutations.
4. Add lifecycle operations with ownership checks, allocation coordination, verification, and interruption recovery.
5. Add foreground native execution and optional application files using the same identity and binding rules.

Use ordinary Go types and functions across these boundaries. Introduce narrow interfaces only where an actual external dependency needs substitution; do not build a generic backend framework or mirror every SDK method.

Before implementing each slice, settle its concrete contracts: exact manifest syntax and native/container binding selection, registration and move/recovery behavior, target identification, allocation policy and supported dependency cycles, generated-file consumption, and machine-readable output schemas. The architecture defines their constraints without pretending these interfaces are already finalized.

## Out of scope for the first version

- A daemon, detached native-process supervisor, or generic workflow/plugin engine.
- A separate Docker `engine` abstraction or replacement Compose orchestrator.
- Cloud-resource provisioning or cleanup.
- Repository-wide search-and-replace of addresses or tracked application configuration.
- Mandatory generated Compose YAML or a second database of Docker resources.
- A shared HTTP/WebSocket gateway, automatic TLS setup, or OAuth/CORS reconfiguration.

A gateway and separately advertised URLs are deferred until there is a concrete need. The current schema defines only container and host addresses.

## References

- [Worktree isolation and endpoint bindings](reports/worktree-isolation-and-endpoint-bindings.md)
- [Go architecture and local state](reports/go-architecture-and-state.md)
- [Go static analysis baseline](reports/go-static-analysis-2026.md)
- [Official Compose SDK](https://docs.docker.com/compose/compose-sdk/)
- [Compose runtime queries (`Ps`, v5.6.1)](https://github.com/docker/compose/blob/v5.6.1/pkg/compose/ps.go)
- [Docker Engine SDKs](https://docs.docker.com/engine/api/sdk/)
