# Go architecture and local state

**Status:** Supporting design report, not implemented behavior. See [Trocker architecture](../architecture.md) for the main architecture reference and component responsibilities.

This design complements [worktree isolation and endpoint bindings](worktree-isolation-and-endpoint-bindings.md), which defines the explicit-first manifest, endpoint representations, workspace ID, and external-resource boundary.

## 1. Architecture decision

Build a **daemonless Go CLI using the official Docker Compose SDK**. Trocker owns workspace identity, isolation planning, endpoint bindings, and ownership verification. Compose owns loading and executing Compose projects; Docker owns the actual runtime resources.

```text
trocker.yaml + Compose files + supplied environment
                         ↓
               Compose SDK LoadProject
                         ↓
       Validate explicit endpoints and bindings
                         ↓
    Workspace identity + registry + Docker observations
                         ↓
                Pure isolation planner
                         ↓
       Transformed in-memory Compose project
          + optional application config files
                         ↓
                 Compose SDK Up
                         ↓
      Inspect resources, mappings, and readiness
                         ↓
     Report verified endpoints; remember preferences
```

Do not introduce a Trocker background daemon, reimplement Compose orchestration, or maintain a second database of Docker's state in the first version.

**Responsiveness is a core requirement:** Trocker must remain a thin coordination layer, not become the bottleneck in front of Docker. Measure and minimize its incremental overhead; long-running commands should be dominated by necessary downstream work such as image pulls, builds, container operations, or application readiness, not avoidable Trocker processing. See [performance and responsiveness](#9-performance-and-responsiveness) for the execution rules and measurement gate.

## 2. Compose integration

Use the [official Compose SDK](https://docs.docker.com/compose/compose-sdk/) for project loading, validation, builds, startup, recreation, and teardown. Its documented integration uses `compose.NewComposeService`, `LoadProject`, and `Up` with a typed Compose project.

Load through the SDK so includes, interpolation, environment files, profiles, and path resolution follow Compose semantics. Transform the loaded project in memory to apply:

- A workspace-specific project name.
- Namespaced container names and build-image tags where Trocker owns them.
- Workspace ownership labels on managed resources.
- Host-port assignments, preserving container ports and network protocols.
- Resolved environment bindings and `TROCKER_WORKSPACE_ID`.
- Explicitly configured mounts for generated application files.

Replace published-port entries in the project model rather than appending a second publication alongside a conflicting original. Validate the transformed model before applying it. Never mutate the checked-in Compose files.

Use Compose's project-scoped queries for actual resource ownership, published mappings, and status. Delegate underlying Docker inspection to the SDK; use targeted Docker SDK calls inside the same integration only for requirements Compose does not expose. Initialize all SDK access from the same resolved Docker configuration and context; do not let separate client defaults accidentally select different daemons. Record runtime preferences against the actual target, not merely a user-editable context name. If a target cannot be identified or reached, fail rather than falling back to another daemon.

Pin a compatible set of Compose, Docker CLI library, and Engine client dependencies. The SDK documentation calls out version compatibility constraints; embedding the SDK does not make those libraries independently interchangeable. Trocker does not need to shell out to the Compose executable for normal lifecycle operations.

Use the SDK's progress event processor for human-readable progress and machine-readable output. Progress events are not a durable resource inventory; inspection remains authoritative after interruption.

## 3. Planner and lifecycle coordinator

The planner receives validated configuration, workspace identity, remembered preferences, and observations. It returns the desired project transformations, resolved bindings, and required actions without performing Docker or filesystem operations.

Keep these concepts distinct:

| Concept | Meaning |
| --- | --- |
| Workspace | Persistent UUID and association with a Git worktree. |
| Runtime target | Docker daemon and host on which ports are published. |
| Endpoint | Explicit service, container port, scheme, and context-specific addresses. |
| Plan | Proposed isolation/configuration changes and lifecycle actions. |
| Observation | Resources and mappings actually found on the target. |

`trocker plan` and `trocker up` use the same planning logic. A plan is a preview, not a port reservation or permission to mutate resources later. `up` refreshes observations and revalidates ownership and allocation before execution.

Binding expressions should support explicit references such as `endpoints.api.container.url` and `workspace.id`, not arbitrary code or shell execution. Missing required endpoint fields and unresolved references are errors, not opportunities for runtime inference.

The lifecycle coordinator sequences I/O and applies the plan. Keep interfaces at external boundaries—Compose, Docker inspection, state storage, and native processes—rather than creating an interface for every domain type.

### Compose-compatible lifecycle commands

Keep command names, defaults, and lifecycle meanings as close to Docker Compose as possible. Trocker adds workspace selection, isolation, and ownership checks; it does not redefine stopping as removal or removal as forgetting a workspace.

| Command | Behavior |
| --- | --- |
| `trocker up` | Create or recreate the required containers and networks, apply configuration, and start the stack. |
| `trocker start` | Start existing stopped service containers without recreating them or applying configuration changes. |
| `trocker stop` | Stop running service containers without removing them. Containers, networks, and volumes remain. |
| `trocker down` | Stop and remove the project's service containers and managed networks. Preserve volumes by default. |
| `trocker down --volumes` / `trocker down -v` | Also remove the project's declared managed named volumes and anonymous volumes attached to its containers, after verifying ownership. |

Use the corresponding Compose SDK lifecycle operations. `start` must not silently reallocate ports or rewrite configuration on existing containers; if their published ports are occupied, report the conflict and direct the user to `up` for reconciliation. Configuration changes also require `up` rather than `start`.

Images and bind-mounted files are not deleted by default. External Docker networks and volumes are never removed by `down`, including with `--volumes`. Retained anonymous volumes are not automatically reattached by a subsequent `up`; persistent application data should use named volumes or explicit bind mounts, following Compose semantics.

All these commands preserve `TROCKER_WORKSPACE_ID` and its registry association, including `down --volumes`. Forgetting a workspace is a separate metadata operation, not an implicit side effect of stack teardown. No lifecycle command cleans up application-owned cloud resources.

## 4. What is authoritative?

| Information | Source of truth |
| --- | --- |
| Desired application configuration | Checked-in `trocker.yaml` and Compose files, resolved with the supplied environment. |
| Workspace identity and checkout association | Trocker's persistent registry. |
| Remembered host-port preferences | Trocker's registry; these are hints, not proof of availability. |
| Actual resources, ownership labels, mappings, and health | Docker inspection. |
| Rendered application configuration | Disposable output derived from configuration and resolved bindings. |

Do not persist running status, health, or actual bound ports as authoritative facts. A container may be changed outside Trocker. `endpoints` must distinguish verified mappings from planned addresses and must not report a remembered port as a live endpoint.

This is not Terraform-style resource state. Trocker stores the identity that Docker cannot preserve after all resources are removed, plus a small amount of allocation metadata for stability and coordination.

## 5. Storage location and contents

Store local metadata in a user-level directory shared across repositories:

| Platform | Default state directory |
| --- | --- |
| Linux | `$XDG_STATE_HOME/trocker`, or `~/.local/state/trocker` when unset. |
| macOS | `~/Library/Application Support/trocker`. |
| Windows | `%LOCALAPPDATA%\Trocker`. |

```text
<state-directory>/
    registry.json
    locks/
    generated/
        <workspace-id>/
            <runtime-target-id>/
                <configured-output-path>
```

`generated/` is optional: an environment-only application needs no generated configuration files. The directory layout is local implementation state, not a public API that applications should hardcode.

### Registry

Use a versioned JSON document, written atomically under a process lock. Its minimal contents are:

- Workspace UUIDs and their Git worktree associations.
- Runtime target identities for each workspace.
- Preferred host-port bindings, including the address and protocol needed to distinguish allocations.
- Pending allocation claims only where needed to coordinate simultaneous startup attempts.

Workspace association must distinguish independent clones and worktrees. A supported worktree move updates that association without changing the UUID; a branch switch does not create a new workspace. If metadata is missing or ambiguous, require explicit recovery instead of silently adopting existing resources.

The registry survives `stop`, `down` (including `--volumes`), container recreation, and removal of all Docker resources. This is necessary because `TROCKER_WORKSPACE_ID` must remain stable for application-owned namespaces even when Docker has nothing left to inspect. Explicitly forgetting a workspace ends its identity; a newly registered replacement gets a new UUID.

Port preferences improve URL stability but are not required for correctness. Reuse them only when compatible with the observed resources and available bindings. Never reuse an identity merely because its old port allocation is free.

Do not store resolved secret environments, credentials, or complete Compose snapshots in the registry. Reject unsupported registry versions or corrupt contents with a recovery error rather than resetting the registry and losing ownership information.

Start with JSON and atomic file replacement; introduce a database only if actual state-management complexity justifies it. An explicit operation journal is not required for v1.

### Generated files

**No generated Compose override is required.** The SDK accepts the transformed project in memory.

Render files only when an application explicitly consumes file-based configuration. A manifest `files[].output` is relative to the workspace/runtime generated directory, not the source checkout. Resolve the template input relative to the manifest. Reject output paths that escape the generated directory, including through symlinks.

Trocker supplies the resolved file path through an explicitly configured mount or process argument. These outputs are disposable: regenerate them from the manifest, templates, and bindings, and do not use them to reconstruct ownership.

Keep state directories private to the user and generated files containing sensitive data readable only by that user, using platform-appropriate permissions. Do not log resolved secrets or include them in ordinary plan output. Remove generated outputs only when they are no longer needed by managed consumers; invalidate them when forgetting their workspace.

A local generated file is not automatically available on a remote Docker host. Until an explicit remote file-transfer mechanism is supported, reject generated bind mounts whose source is not accessible on the target instead of assuming the same path exists there.

## 6. Concurrency and recovery

Use a workspace/runtime lock to serialize conflicting lifecycle operations. Use a short-lived shared registry/allocation lock to coordinate updates across repositories. Adopt a consistent lock order and do not hold the global lock during image builds or other long Docker operations.

For planned ports, record a pending claim before releasing the allocation lock so another Trocker invocation does not select the same binding while startup is in flight. Claims need an owning operation/process identity and stale-claim recovery. A PID alone is insufficient because the OS may reuse it; reconcile abandoned claims with Docker observations before making an allocation reusable.

Locks and claims coordinate Trocker invocations sharing the registry. They do not reserve sockets against other programs, other OS users, or another machine targeting the same daemon. Docker's or the native application's successful bind is authoritative. Never terminate an unrelated listener to obtain a port.

A startup attempt should:

1. Lock the workspace/runtime and validate its identity.
2. Load configuration and inspect the target's existing resources.
3. Validate ownership and plan candidate allocations under the allocation lock.
4. Resolve bindings and transform the in-memory Compose project.
5. Render any explicitly requested application files.
6. Apply through Compose, then inspect actual mappings and configured readiness checks.
7. Remember confirmed preferences, release pending claims, and report verified endpoints.

On a genuine bind conflict, reallocate and regenerate affected configuration. Recreate or restart affected consumers before reporting the new endpoint as ready. Other errors must not be treated as port conflicts. Unsupported cyclic public-endpoint dependencies fail before dependent consumers are launched.

If a process crashes or startup partially fails, discover surviving resources by workspace labels on the next invocation and reconcile them with the desired project. Do not assume nothing was created, blindly adopt matching names, or delete persistent volumes as rollback. Missing or conflicting ownership is an actionable error, not permission to proceed.

`status`, `endpoints`, and `doctor` are observational: they do not sweep stacks or delete data. `stop` leaves containers in place; `down` removes containers and managed networks but preserves volumes unless `--volumes` is requested. Both preserve registry identity. Destructive cleanup requires explicit intent and verified ownership. Worktree deletion or PR merge alone does not authorize volume deletion.

## 7. Native processes and future gateway

Start native support with foreground `trocker run`: inject endpoint bindings and `TROCKER_WORKSPACE_ID`, forward signals, and track the managed child for the duration of the command. Its host-port use participates in allocation coordination; do not reclaim an allocation while an owned native process still uses it. Never identify a process to terminate solely by its listening port or a stale PID.

Do not add detached-process supervision or a background Trocker daemon in v1. If those become necessary, define ownership and recovery for them explicitly rather than treating them as a trivial extension of Docker cleanup.

A shared HTTP/WebSocket gateway is a later component. The planner's separate host and public endpoint representations allow it to be added without changing what application bindings mean. A gateway must verify destination ownership and cannot infer application-specific OAuth or cookie configuration.

## 8. Go package boundaries

```text
cmd/trocker/
internal/
    cli/          # Commands and human/machine-readable output.
    config/       # Manifest parsing and validation.
    workspace/    # Persistent identity and Git discovery.
    plan/         # Pure isolation decisions and binding resolution.
    lifecycle/    # Apply, recover, stop, and verify.
    compose/      # Compose SDK operations and runtime observations.
    state/        # Registry persistence and process locks.
    native/       # Foreground managed processes.
```

Keep domain types small and package boundaries driven by responsibilities. The planner must not depend on command-line parsing or perform network/filesystem I/O. SDK types can stay at the Compose boundary where practical without duplicating the full Compose schema in Trocker.

## 9. Performance and responsiveness

Performance is part of the architecture, not a later optimization pass. A small stack must feel immediate, and a large repository or many unrelated worktrees must not turn every command into a discovery or maintenance job. Ownership validation, secret handling, and verified endpoint reporting remain mandatory; speed does not justify skipping them.

### Keep each command's path minimal

- `help` and `version` must not initialize Docker clients, contact the daemon, inspect Git, or load the workspace registry. Initialize expensive dependencies lazily.
- Ordinary commands resolve only the current workspace and required configuration. Do not recursively scan repository contents, visit every registered worktree, query Git hosting or PR status, check for updates, or invoke an LLM. Rich discovery belongs to explicit onboarding or diagnostics, never the normal lifecycle path.
- `status` and `endpoints` inspect the requested workspace's resources and load only the metadata needed to interpret them. They do not run the startup planner, render files, allocate ports, or wait for application readiness.
- `start`, `stop`, and `down` use the corresponding SDK operation with the minimum required configuration and ownership checks. They do not run `up`'s binding generation, allocation, or build workflow as a prerequisite.
- `up` loads the Compose project once per configuration attempt and reuses that model through validation, transformation, and execution. Avoid duplicate parsing and YAML/JSON round trips. A retry may replan affected allocations without redoing unrelated discovery.

### Avoid unnecessary I/O and serialization

- Reuse the resolved target and clients within an invocation. Prefer filtered or batched Docker queries over listing and individually inspecting every resource on the daemon. Refresh observations at mutation boundaries where correctness requires it, not redundantly at every layer.
- Run independent I/O with bounded concurrency and let Compose retain its own dependency-aware build/start scheduling. Do not wrap SDK operations in an additional serial per-service loop.
- Keep registry/allocation critical sections short. Builds, pulls, container readiness, and unrelated workspace operations must not wait behind one global lock. Measure lock contention as part of Trocker overhead.
- Prefer events and SDK readiness mechanisms over adding fixed sleeps. When polling is unavoidable, scope it to the resources being awaited; respect cancellation and surface which dependency is still pending.
- Render only explicitly configured application files, and do not rewrite unchanged contents. Keep project transformations deterministic: timestamps, random per-run names, or unstable ordering must not cause needless container recreation.
- Preserve Docker/BuildKit cache reuse. Workspace-specific image tags must not imply `--no-cache`, forced rebuilds, or automatic workspace-specific build arguments when the application only needs runtime identity.
- Do not persist resolved-project caches by default. If profiling justifies caching, define invalidation for Compose inputs, profiles, environment, and target configuration; never serve stale ownership or actual-port observations as a speed shortcut.

The initial JSON registry is a simplicity choice, not a performance guarantee. Benchmark it with many registered workspaces. If parsing or rewriting the whole registry becomes material, change the storage layout rather than hiding that cost behind a background daemon.

### Measure Trocker's overhead separately

Provide opt-in phase timings for workspace/config loading, planning and binding resolution, lock wait, state I/O, Docker requests, builds/pulls, readiness, and verification. Stream progress promptly so a slow dependency is visible. Timings and diagnostic output must not expose secrets.

Compare end-to-end command latency with an equivalent direct Compose SDK execution using the same resolved workload and dependency versions. Measure cold and warm runs, no-change `up`, lifecycle commands, and status queries across small and larger stacks. Include many registered workspaces, simultaneous independent startups, and a contended workspace. A warm no-change `up` must not trigger builds or recreations solely because Trocker ran.

Report sample counts, environment, and latency distributions, not a single best-case run. Account for the critical path when work overlaps; summing concurrent phase durations does not measure user-visible latency. In-process SDK initialization and Compose loading contribute to CLI latency too: do not classify all time inside the SDK as unavoidable Docker-daemon wait.

Set numeric overhead budgets and regression thresholds from the first representative measurements, with documented headroom. No latency numbers have been measured yet, so this proposal does not invent a millisecond guarantee. Establish and enforce those budgets before claiming the first version meets the responsiveness requirement; performance regressions belong in release validation alongside correctness.

## Scope of the first version

- Explicit endpoint and binding configuration with discovery-assisted initialization.
- Stable workspace identity and SDK-backed local stack isolation.
- Collision-aware publication, ownership checks, and verified endpoint output.
- A minimal local registry and optional application-file rendering.
- Foreground native-process execution.
- Measured command overhead and performance regression budgets, with no automatic maintenance on ordinary command paths.

Not included: a background daemon, mandatory generated Compose files, a Docker resource-state database, cloud-resource provisioning, external-resource cleanup, detached native supervision, or an automatic HTTP gateway.

## References

- [Docker Compose SDK](https://docs.docker.com/compose/compose-sdk/): official Go integration, project lifecycle operations, dependency compatibility, and progress events.
- [Compose stop](https://docs.docker.com/reference/cli/docker/compose/stop/) and [Compose down](https://docs.docker.com/reference/cli/docker/compose/down/): lifecycle terminology, default retention, and explicit volume removal.
- [Compose Go library](https://github.com/compose-spec/compose-go): typed Compose model and loading semantics used by the Compose ecosystem.
- [Docker Engine SDKs](https://docs.docker.com/engine/api/sdk/): resource inspection and Engine access; select client versions compatible with the embedded Compose SDK.
