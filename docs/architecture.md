# Trocker architecture

Trocker is a daemonless Go CLI for isolated Docker Compose stacks per Git worktree. It owns workspace identity, isolation policy, and explicit application bindings; the official Compose and Docker SDKs own container orchestration and runtime discovery. See the [README](../README.md) for installation and usage.

## Components and boundaries

| Package | Responsibility |
| --- | --- |
| `cmd/trocker` | Process entry point and exit status |
| `internal/cli` | Cobra commands, argument validation, diagnostics, and result presentation |
| `internal/config` | Strict manifest parsing and structural validation |
| `internal/workspace` | Git discovery and workspace identity rules |
| `internal/state` | Registry, process locks, port leases, and generated-file publication |
| `internal/plan` | Pure isolation transformations and binding decisions |
| `internal/compose` | Compose loading, Docker target resolution, SDK operations, and observations |
| `internal/lifecycle` | Command workflows, ownership checks, allocation, and effect coordination |
| `internal/generated` | Confined template loading, rendering, and output validation |
| `internal/native` | Foreground native execution and process-group cleanup |

Keep decisions separate from effects. The planner does not call Docker, read files, acquire locks, or persist state. Lifecycle workflows coordinate named steps with explicit mutation and cleanup boundaries. CLI types and diagnostic rendering stay in `internal/cli`.

Delegate Compose parsing, includes, interpolation, `.env` handling, dependency ordering, builds, convergence, and progress rendering to the SDK. Use targeted Docker SDK operations where Compose does not expose the required ownership or retained-resource information. There is no separate engine abstraction, replacement orchestrator, or generated Compose override: transformations apply to an in-memory project.

Initialize dependencies only when needed. Help, version, and static completion do not contact Docker or load workspace state. Normal commands do not scan all repositories, contact Git hosting, or perform automatic maintenance; `init` is the exception, and it only releases registrations it can prove are inert.

## Identity and sources of truth

A workspace is a Git checkout or linked worktree associated with a persistent random UUID. Branch names, paths, ports, and Docker context names are not identities.

Git discovery uses the installed Git executable and canonical checkout, per-worktree Git, and common Git directory paths. Repository-local Git environment overrides are removed from discovery subprocesses; ordinary Git ownership checks remain in effect.

Registration stores the UUID and daemon associations in the user-level registry and writes a matching `trocker-workspace-id` marker in the per-worktree Git administrative directory. The registry is committed before the marker so a confirmed retry can finish registration without changing identity. A marker without its registry entry is an error, not permission to adopt resources.

Confirmation has two forms. The terminal prompt shows the proposal and reads a yes/no answer. The digest form exists for callers without a terminal: `init --dry-run` writes nothing and reports a SHA-256 over the decisions the proposal asks for, and `init --confirm <digest>` registers only while a freshly computed digest still matches. The digest covers the effective manifest (proposed or existing), the Engine-reported target identity and its host, whether the manifest would be created, and whether a moved checkout would be relinked. It deliberately excludes the checkout path and the workspace identity, so one approval covers every worktree that shares the approved configuration, while editing the manifest or switching daemons invalidates it. Identity is still assigned at apply time and is never part of an approval.

Repeated initialization, branch switches, and teardown preserve the UUID. A linked-worktree move is supported when the Git administrative directory is unchanged, the old checkout is absent, and Git identifies one new checkout. Read-only commands report that relocation needs confirmation; `init` persists it. Ambiguous moves, missing associations, and corrupt state fail without resetting identity. Automatic recovery is not implemented.

Registration tracks the worktree, not the stack, so stack lifecycle commands never delete it. `init` releases registrations whose checkout directory and Git administrative directory are both absent and which own nothing on the selected target. A checkout that is absent while its Git administrative directory survives is also what an interrupted `git worktree move` looks like, so it is never swept. An explicit `trocker release` resolves the current workspace or any unambiguous id prefix from any directory, refuses before changing anything when retained volumes are not being removed, stops what the workspace still owns through the same ownership-checked lifecycle path as `down`, and then deletes the marker before the registry entry. That order is the recoverable one: a marker without its entry blocks registration with no supported recovery, while an entry without its marker is repaired by `init`. Inspecting resources that cannot be reached fails the command rather than releasing ownership that may still be in use.

The Compose project name is `trocker-` followed by the UUID without hyphens. Managed resources carry `io.trocker.workspace-id` alongside Compose ownership labels. Managed containers and native children receive the authoritative `TROCKER_WORKSPACE_ID`. This UUID is non-secret metadata, not authentication against another Docker client.

Docker configuration/context/environment resolution belongs to the SDK. Target associations use the Engine-reported daemon identity, not the context alias. Aliases for one daemon share an association; a replaced daemon needs explicit confirmation. Failed target resolution never falls back to another daemon.

| Information | Authority |
| --- | --- |
| Workspace and target associations | Registry, checked against Git association evidence |
| Desired services | Compose project plus explicit Trocker configuration |
| Existing resources and publications | Live SDK observations |
| Proposed transformations | Pure plan; not proof of runtime state |
| Stable port assignments | Registry contract; claimed exactly at startup |
| Remembered ports | Preferences; not reservations or observed endpoints |
| Generated-file index | Cache hint; not evidence that old files are unused |

## Configuration and bindings

The workspace-root `trocker.yaml` is a single, strictly validated YAML document. Unknown fields, duplicate keys, unsupported versions, and invalid references fail with safe field paths or locations. Commands below the root use the same manifest.

```yaml
version: 1
compose:
  - compose.yaml
profiles:
  - workos-local
stable_ports: true
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
    native:
      environment:
        API_URL: "{{ endpoints.api.host.url }}"
```

`compose` is a nonempty ordered path list; relative paths resolve from the manifest directory. Compose contents and merging are SDK concerns. `endpoints` and `bindings` are optional. Binding service names must exist in the active Compose project.

`profiles` is an optional ordered list of Compose profile names. A nonempty list replaces `COMPOSE_PROFILES` from the environment or project `.env`; `*` activates every profile. An empty or omitted list preserves the environment fallback, and `profiles: []` is therefore the same as omitting the field. Profile names use Compose's `[A-Za-z0-9][A-Za-z0-9_.-]*` shape, duplicates are rejected, and an explicit name that no Compose service declares fails with the available profiles. The `--profile` flag on `plan`, `doctor`, `up`, `run`, `status`, and `endpoints` is a one-command override that replaces the manifest selection. Commands that operate on existing resources (`start`, `stop`, `down`, `release`, `ls`, `ps`, `logs`, `exec`) do not load Compose and remain profile-agnostic. `plan` and `doctor` report the active profiles and resulting service count.

`stable_ports` is an optional boolean. It is false by default, preserving ephemeral host-port allocation. When true, every selected TCP publication receives one persisted workspace-specific host port, including publications created from endpoint `expose` declarations. New assignments prefer the current publication, a remembered preference, or the concrete port Compose already resolved, then the first free ports of `stable_port_range`, so enabling the policy adopts existing values without a manual migration unless they sit in the host's ephemeral range. `stable_port_range` is an optional inclusive `first-last` range from 1024 through 65535, valid only with `stable_ports`, and defaults to `20000-32767`, below every common OS ephemeral range. `init`, `plan`, and `doctor` reject a range, declared or default, that overlaps the host's ephemeral range: read from the kernel on Linux and macOS, and IANA's 49152-65535 elsewhere. Once assigned, the value is authoritative: `up` claims it exactly, a conflict fails, and `down` retains it. `trocker ports` reports assignments without loading Compose or observing containers. A profile-gated publication is assigned when its service becomes active. `release` removes the target's assignments.

Endpoint aliases match `[a-z][a-z0-9_]*`. Every endpoint declares a service, a container port from 1–65535, and a scheme (`http`, `https`, `ws`, `wss`, or `tcp`). Its port must match a TCP `ports` target or `expose` declaration. Schemes describe existing listeners; they do not configure TLS or prove readiness.

Bindings are literal strings with restricted substitutions:

- `workspace.id`
- `endpoints.<alias>.<container|host>.<host|port|url>`
- `files.<identifier>.path`, only in the declaring consumer's environment

There are no arbitrary environment lookups, expressions, shell execution, or recursive substitutions. `tcp` endpoints expose host and port, not URL. Container addresses use service DNS and the declared container port; consumers must share a supported network. Host addresses use verified or coordinated local loopback publications. A browser-facing host URL is not a container-to-container address.

Container bindings override Compose environment values. Native bindings override the invoking process environment and never implicitly inherit Compose values. Workspace identity is applied last; conflicting explicit configuration is rejected. Native endpoint references must use host addresses. Runtime bindings do not provide frontend build-time substitution.

## Isolation and ownership

The supported target is a local Docker daemon running Linux containers, including Docker Desktop. Publications use IPv4 loopback TCP.

The planner assigns workspace-scoped resource names and build tags, removes `container_name`, replaces host publications, applies bindings, and publishes explicitly declared endpoints that use only `expose`. Container ports remain unchanged.

Unsupported configurations fail before mutation rather than silently sharing resources. These include remote targets, UDP/IPv6 publications, nonstandard network modes, external or explicitly named networks/volumes, custom networking drivers/addressing, explicit anonymous mounts, deploy/replication settings, hooks/providers, privileged access, undeclared host namespace/capability/device access, user-declared secrets/configs/models, build tags/network/ssh/secrets/entitlements, additional build contexts not declared in `build_contexts`, and Docker socket access not declared in `docker_socket_services`. Read-only bind mounts and managed named volumes are supported. Built service names must be lowercase.

Writable bind mounts are allowed only for host paths declared under `read_write_paths` in the manifest. Each path resolves against the manifest directory and must stay inside it, so a declaration cannot expose the rest of the filesystem; the check is lexical and does not follow symlinks. A mount source is matched against the declared path by components, so a sibling with a shared name prefix is not covered. `plan` reports every writable mount before mutation, and an undeclared mount fails with recovery guidance instead of a generic policy message.

Host paths outside the checkout are a separate opt-in under `shared_write_paths`, because they are not workspace state: the host shell and every other workspace read and write them too. A leading `~/` expands to the home directory; environment expansion, globbing, and named home directories are rejected. A resolved declaration must be absolute and must neither sit inside the checkout nor contain it, and the filesystem root and the home directory itself are refused. The check is lexical and does not follow symlinks, and it does not inspect the filesystem, so read-only commands never depend on the host's state. Mounts authorized by a shared path are planned with `create_host_path: false`, so a missing directory fails at mount time instead of Docker silently creating it. Trocker never creates, labels, or removes these paths; `down`, `down --volumes`, and `release` leave them untouched. `plan` reports them separately from workspace writable mounts and states how many mounts are shared.

Additional image build contexts follow the same rule. Only host paths declared under `build_contexts` may be read by Compose `build.additional_contexts`; each path resolves against the manifest directory and must stay inside it, with the same lexical component-wise matching as `read_write_paths`. Compose normalizes local context values to absolute paths before planning, so a value that is not absolute is a service reference or a remote source and is rejected. Those diagnostics name the context but never repeat its value, which can carry credentials. `plan` reports each declared context. `build.context` and `build.dockerfile` are deliberately not gated: Compose already lets them name any host path, and gating them needs its own declaration. `build_contexts` governs the additional reads only.

Docker API access follows the same explicit-policy rule. Only services listed under `docker_socket_services` may expose the daemon socket, through a bind mount or Compose `use_api_socket`. Trocker recognizes a bind source named `docker.sock` and mounts of the conventional socket paths or their parent directories. Read-only socket mounts are gated the same way because mount mode does not prevent connecting to the socket. Detection is lexical; a socket reached through a differently named path or a symlink is not recognized. `plan` reports Docker API access separately from writable host paths. This access is host-level and cannot be workspace-isolated.

Host namespace, capability, and device access follows the same rule. Only services with a matching `host_access` grant may use `pid: host`, `ipc: host`, Compose `cap_add`, or `devices`. The grant lists the exact namespaces, capabilities, and device source/permissions allowed, so a later Compose edit cannot silently broaden access. `privileged`, `device_cgroup_rules`, `uts`, and `cgroup` remain rejected. `plan` reports each active grant. These grants are host-level and cannot be workspace-isolated.

Existing resources must match both Compose and workspace ownership. Familiar names, ports, or missing labels never authorize adoption or deletion. Ownership checks include networks and volumes even though `status` currently lists only containers. Isolation does not partition external databases, queues, buckets, browser cookies, or cloud resources; applications own those concerns.

## Command workflows

| Command | Workflow and mutation boundary |
| --- | --- |
| `init` | Discover Git, validate existing configuration or propose minimal SDK-selected Compose files, resolve the target, show changes, confirm, then persist configuration/registration. With `stable_ports`, also assigns missing host ports for active services after registration. Confirmation is a terminal prompt or a digest accepted with `--confirm`; `--dry-run` reports the same proposal and digest and writes nothing. Does not infer endpoints/bindings or create Docker resources. |
| `plan`, `doctor` | Load confirmed identity, configuration, and relevant observations; validate and plan without writes, allocation claims, or rendered output. Stable assignments already in the registry are shown; missing ones are reported as pending, and assigned ports inside the host's ephemeral range or held by another program are reported with the command that moves them. `doctor` checks the supported subset, not general security. |
| `up` | Lock the workspace/target, load and validate, observe ownership, ensure stable assignments, plan and allocate, render selected container files, apply through Compose, verify publications, and remember confirmed mappings. |
| `start`, `stop`, `down` | Reconstruct existing resources through the SDK, verify ownership, and perform the lifecycle operation without reloading current Compose/manifest files. Registration is preserved. |
| `release [WORKSPACE]` | Resolve the workspace by directory or id prefix, verify the target association, stop owned resources through the `down` path, then delete the worktree marker and registry entry. Retained volumes keep the registration unless `--volumes` is set. Releasing an already-released workspace is a successful no-op. |
| `status`, `endpoints` | Query owned resources without changing files or Docker state. Unregistered workspaces fail rather than registering implicitly. |
| `ports` | Read persisted stable host-port assignments for the current workspace and target. Does not load Compose, observe containers, claim ports, or acquire locks. |
| `ports reassign` | Under the workspace lock, move named assignments, or without arguments every assignment inside the host's ephemeral range, to free ports in `stable_port_range`. Refuses while a named service is running, so `ports` never reports a value its container does not publish; the next `up` recreates the containers on the new ports. Does not touch containers or volumes. |
| `ps` | Read registration and owned containers, then list or encode them as JSON without loading current Compose/manifest files, changing Docker state, or acquiring locks. Default output includes running non-one-off containers; `--all` adds stopped and one-off containers. |
| `logs`, `exec` | Read owned containers, resolve requested Compose service names against that snapshot, and stream logs or run a command in the selected container without acquiring workspace locks. `exec` requires a running container and propagates its exit status. |
| `ls` | Read the registry, checkout liveness, and live containers for the selected Docker target; list stacks with their worktree paths and mark registrations whose worktree is gone, including whether Git metadata still records it. Does not load manifests, run Git commands, or acquire locks. |
| `run` | Validate the selected service, observe referenced host endpoints, prepare native bindings/files, then launch the foreground child without holding workspace/registry locks. |

`up SERVICE...` uses Compose's dependency closure. Services referenced by bindings must be selected or declared in `depends_on`; bindings do not invent readiness dependencies. Detached startup waits up to two minutes for Compose running/healthy readiness. Foreground startup streams logs and holds the workspace mutation lock until the session ends.

`start` preserves existing container configuration and generated mounts; use `up` to apply changes. Ephemeral Docker-assigned ports may change after restart; stable assignments remain exact. `stop` retains resources. `down` removes owned containers/networks but retains volumes; `--volumes` additionally removes verified managed named and attached anonymous volumes using Engine semantics. Unattached anonymous volumes are not swept. Images, external resources, bind-mounted files, generated files, and workspace registration remain; `down` is a stack command, and only `trocker release` deletes a registration.

Compose recreation prompts are never automatically approved, orphans are not automatically removed, and failed startup does not roll back volumes. Reinspect after partial failure and retry after correction; do not assume that nothing was created.

## Port allocation and locking

Prefer Docker-assigned ports whenever consumers can wait for discovery. Container bindings or file inputs requiring a host port/URL before startup need coordinated assignments. Native bindings consume already-running publications and do not allocate ports.

Without `stable_ports`, reuse verified mappings first, then available remembered preferences, then OS-selected ephemeral candidates. Probe conservatively for loopback and wildcard conflicts. A probe or lease is not a socket reservation against unrelated programs; Docker's successful bind is authoritative.

With `stable_ports`, the registry assignment replaces preference-based selection for every publication. Missing assignments are created from the current publication, a remembered preference, or the concrete Compose value when available, otherwise from `stable_port_range`: the first contiguous run that fits every missing publication, or else its first free ports. Trocker never asks the OS for a stable port, because the OS picks from the ephemeral range, where programs listening on port zero would later make the exact claim fail. For the same reason a current or remembered value inside that range is not adopted; an explicit Compose value is kept wherever it is. The resulting host ports are unique across all persisted assignments, and dynamic allocation avoids every stable assignment even while its workspace is stopped. Startup claims each exact port, probes loopback and wildcard listeners, and fails on conflict; it never retries a stable port with an ephemeral replacement. The retry-with-new-bindings path applies only to dynamic ports. Assignments survive `down` and are deleted with the target by `release`; publications removed from Compose do not prune assignments automatically.

Per-port OS-backed file locks coordinate concurrent Trocker processes across local daemon identities sharing the same user state directory. Process exit releases claims; stale lock files do not require PID-based adoption or deletion. Limits are 32 candidate probes per publication and three startup attempts. Retry only recognized Engine bind failures corroborated by owned-container failed-start state. Refresh observations, assignments, bindings, and rendered files on retry; never terminate a competing listener or retry unrelated errors as bind conflicts.

Lock scopes are deliberately separate:

- Initialization uses a Git-administrative-path workspace lock, then a short registry lock; prompting and Docker discovery happen before locking.
- Workspace/target mutation locks serialize changes to a stack.
- Per-port leases span allocation/startup verification or the foreground session.
- Registry locks protect short metadata updates, never builds, pulls, readiness waits, or child lifetimes.
- Generated-file consumer locks protect only generation comparison/publication.

Only verified publications become remembered preferences. Stable assignments are configuration contracts, not live observations: `plan` may show their values but never claims them, and `endpoints` still reports only observed publications. Plans and read-only commands neither claim ports nor present remembered/proposed addresses as live endpoints.

## Persistence

| Platform | State directory |
| --- | --- |
| Linux | `$XDG_STATE_HOME/trocker`, otherwise `~/.local/state/trocker` |
| macOS | `~/Library/Application Support/trocker` |
| Windows | `%LOCALAPPDATA%\Trocker` |

Versioned JSON and atomic writes hold minimal workspace/target associations, stable port assignments, and port preferences. OS locks coordinate processes. Unix state is private; Windows registry state relies on the user profile's inherited ACL. Corrupt or unsupported state is an error, not an empty registry.

Do not persist a duplicate Docker inventory, full Compose projects, resolved environments, or secrets in registry metadata. Generated application files are the explicit exception for rendered content and have their own private storage policy.

## Generated configuration files

Files are declared under `bindings.<service>.files` for containers or `bindings.<service>.native.files` for native processes. The scopes are separate. Each declaration has a restricted identifier, a `template`, an `output` filename, a `format` (`json` or `text`), and optional string `values`. Container files additionally require an absolute, clean `target`; native files cannot specify one.

Templates must be regular files confined to the manifest directory tree. Output names cannot contain paths, traversal, or colons and must be unique case-insensitively within the consumer. Container targets cannot overlap other mounts/files or special filesystem and Docker-managed hostname/DNS paths.

Rendering uses Go `text/template`, explicit string values, ordinary built-ins, `missingkey=error`, and a custom `json` encoder. It exposes no ambient environment, filesystem, network, or execution helpers. Standard template semantics still apply, including to built-ins such as `index`; templates are trusted configuration, not sandboxed programs. Each input and rendered file is limited to 1 MiB. JSON must be one valid JSON value before publication.

Values use the same identity/endpoint binding grammar as environment bindings; file-to-file references are disallowed. `files.<identifier>.path` resolves only in the matching consumer's environment. Native files require an explicit environment path reference. Container paths resolve to their declared targets, never private host paths.

`init`, `plan`, and `doctor` validate declarations, inputs, syntax, and references without rendering. `run` renders the complete native set before launching. `up` renders selected container sets using that attempt's assignments; file-only host references participate in allocation. `start` never rerenders.

```text
<state>/generated/<workspace-id>/<target-key-or-local>/<native|container>/<service-key>/
    current.json
    <generation-id>/<output-filename>
```

Target and service keys use identity-derived hashes to avoid unsafe path components. Literal/UUID-only native inputs use local scope without contacting Docker. Generation IDs are opaque, not secret-derived content hashes.

Confined `os.Root` operations, private staging, and atomic publication prevent path escapes and partially selectable generations. Identical current output reuses its generation; changed bytes select a new immutable snapshot. Existing consumers keep their original files. Only comparison/publication holds the consumer lock; no lock spans native execution.

Directories are `0700`; metadata and native files are `0600`. Container files are `0444` within private directories and individually delivered through Compose file-backed configs as read-only bind mounts. The daemon must be able to access the private tree. All UIDs in the receiving container can read the file; this is not per-user secret isolation inside a container. No host directory is made public or mounted wholesale by generated-file delivery.

Compose config reference names include opaque generation IDs. Changed content changes the reference and lets Compose's normal service hashing recreate the consumer; unchanged content does not. No custom copy/mount orchestrator or public content digest is needed.

**No generated-file garbage collection is implemented.** Startup failures, stopped containers, parent crashes, and `down --volumes` do not authorize deleting published generations. A released lock or absent parent is not proof that a surviving consumer no longer needs its files. Old secrets therefore remain private but retained. Native and container file delivery support Linux/macOS hosts; Windows delivery is rejected pending tested ACL semantics.

## Native execution

`run SERVICE -- COMMAND [ARG...]` executes an active Compose service's explicitly configured native application, not a one-off container. Dependencies must already have owned, running host publications. Literal/UUID-only inputs need no daemon connection. Preparation creates no containers, port claims, or registry entries.

The child inherits the invoking directory and environment, receives native overrides and workspace identity, and retains stdin/stdout/stderr. `os/exec` uses the parent's PATH; argv is passed unchanged without shell expansion. Applications needing a config flag can explicitly use a wrapper. Endpoint observations are a startup snapshot, not supervision of dependencies.

Linux/macOS use an owned process group and terminal handoff. SIGINT/SIGTERM are forwarded, cancellation escalates after five seconds, and remaining group members are cleaned after leader exit. Exit observation precedes reaping to avoid signalling a reused process-group ID. Terminal ownership/settings are restored. Descendants that deliberately escape the group/session or elevate privileges are outside this contract.

Native execution is not a sandbox, detached supervisor, or allocator for the application's own listening ports. Windows native execution is unsupported.

## Output and failure contracts

Diagnostics, prompts, progress, and `up` foreground container logs go to stderr; results, including `logs` output, go to stdout. Compose's SDK renderer owns resource progress: terminal rendering interactively and plain rendering for pipes/CI, `NO_COLOR`, and `TERM=dumb`. Trocker context is placed before or after live progress, not interleaved. Build-step logs remain suppressed and known SDK failures are classified into safe diagnostics.

Errors have a concise summary and optional action/context. Plans and diagnostics can show binding names and references, never resolved environment values, rendered content, raw secret-bearing configuration, or authentication data. Preserve internal causes without exposing them indiscriminately.

Exit status is 0 for success, 1 for configuration/runtime failures, and 2 for CLI usage errors. Native and container `exec` child exit codes, including 2, propagate without an extra Trocker diagnostic. Signal exits use 128 plus the signal number; explicit child exit codes take precedence, while signal-canceled execution does not become successful merely because the child returned zero.

`ps --json` emits schema version 1 with `workspace_id`, the Compose `project`, and a `containers` array of `name`, `service`, `state`, and `health`. The array is `[]` when nothing matches. It reads only registration and owned live containers, so it does not require current Compose or manifest files.

`ports --json` emits schema version 1 with `workspace_id`, `target_id`, and service/container-port-sorted `ports`. Each entry contains `service`, `container_port`, and `host_port`; the array is `[]` when no stable assignments exist. It reads the registry and target association only.

`endpoints --json` emits schema version 1 with `workspace_id`, `target_id`, and alias-sorted `endpoints`. Each endpoint contains `name`, `service`, `container_port`, `scheme`, `state`, and `addresses` with container/host address objects (`host`, `port`, and `url` except for TCP). States are `published`, `stopped`, or `missing`; stopped/missing publications have null host addresses. Published means an observed running mapping, not application readiness. Ambiguous or replicated mappings and ownership conflicts fail rather than selecting arbitrarily. An unreachable daemon is an error, not a fabricated missing result. JSON failures leave stdout empty.

## Validation and limits

Tests protect Trocker's decisions and safety contracts: pure transformations, identity, read-only behavior, ownership, retention, concurrent allocation/publication, secret-safe failures, and interrupted-operation recovery. Compose loader fixtures live in `internal/compose/testdata/contracts`; integration tests use real disposable resources, never a developer's existing stack. Do not mock SDK calls merely to verify their sequence or retest SDK internals.

Current limits include retained-resource listing, explicit recovery, intentional sharing policies, remote targets, UDP/IPv6 publications, Windows native execution/generated-file delivery, and generated-file cleanup. These are not implicit capabilities. There is no daemon, generic plugin/workflow framework, cloud-resource manager, gateway, automatic TLS, or application-specific OAuth/CORS rewriting.
