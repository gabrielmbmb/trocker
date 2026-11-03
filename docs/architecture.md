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

Initialize dependencies only when needed. Help, version, and static completion do not contact Docker or load workspace state. Normal commands do not scan all repositories, contact Git hosting, or perform automatic maintenance.

## Identity and sources of truth

A workspace is a Git checkout or linked worktree associated with a persistent random UUID. Branch names, paths, ports, and Docker context names are not identities.

Git discovery uses the installed Git executable and canonical checkout, per-worktree Git, and common Git directory paths. Repository-local Git environment overrides are removed from discovery subprocesses; ordinary Git ownership checks remain in effect.

Registration stores the UUID and daemon associations in the user-level registry and writes a matching `trocker-workspace-id` marker in the per-worktree Git administrative directory. The registry is committed before the marker so a confirmed retry can finish registration without changing identity. A marker without its registry entry is an error, not permission to adopt resources.

Repeated initialization, branch switches, and teardown preserve the UUID. A linked-worktree move is supported when the Git administrative directory is unchanged, the old checkout is absent, and Git identifies one new checkout. Read-only commands report that relocation needs confirmation; `init` persists it. Ambiguous moves, missing associations, and corrupt state fail without resetting identity. Automatic recovery and forgetting are not implemented.

The Compose project name is `trocker-` followed by the UUID without hyphens. Managed resources carry `io.trocker.workspace-id` alongside Compose ownership labels. Managed containers and native children receive the authoritative `TROCKER_WORKSPACE_ID`. This UUID is non-secret metadata, not authentication against another Docker client.

Docker configuration/context/environment resolution belongs to the SDK. Target associations use the Engine-reported daemon identity, not the context alias. Aliases for one daemon share an association; a replaced daemon needs explicit confirmation. Failed target resolution never falls back to another daemon.

| Information | Authority |
| --- | --- |
| Workspace and target associations | Registry, checked against Git association evidence |
| Desired services | Compose project plus explicit Trocker configuration |
| Existing resources and publications | Live SDK observations |
| Proposed transformations | Pure plan; not proof of runtime state |
| Remembered ports | Preferences; not reservations or observed endpoints |
| Generated-file index | Cache hint; not evidence that old files are unused |

## Configuration and bindings

The workspace-root `trocker.yaml` is a single, strictly validated YAML document. Unknown fields, duplicate keys, unsupported versions, and invalid references fail with safe field paths or locations. Commands below the root use the same manifest.

```yaml
version: 1
compose:
  - compose.yaml
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

Unsupported configurations fail before mutation rather than silently sharing resources. These include remote targets, UDP/IPv6 publications, nonstandard network modes, external or explicitly named networks/volumes, custom networking drivers/addressing, explicit anonymous mounts, deploy/replication settings, hooks/providers, privileged/device/namespace access, user-declared secrets/configs/models, advanced build access/tags, and Docker socket access not declared in `docker_socket_services`. Read-only bind mounts and managed named volumes are supported. Built service names must be lowercase.

Writable bind mounts are allowed only for host paths declared under `read_write_paths` in the manifest. Each path resolves against the manifest directory and must stay inside it, so a declaration cannot expose the rest of the filesystem; the check is lexical and does not follow symlinks. A mount source is matched against the declared path by components, so a sibling with a shared name prefix is not covered. `plan` reports every writable mount before mutation, and an undeclared mount fails with recovery guidance instead of a generic policy message.

Docker API access follows the same explicit-policy rule. Only services listed under `docker_socket_services` may expose the daemon socket, through a bind mount or Compose `use_api_socket`. Trocker recognizes a bind source named `docker.sock` and mounts of the conventional socket paths or their parent directories. Read-only socket mounts are gated the same way because mount mode does not prevent connecting to the socket. Detection is lexical; a socket reached through a differently named path or a symlink is not recognized. `plan` reports Docker API access separately from writable host paths. This access is host-level and cannot be workspace-isolated.

Existing resources must match both Compose and workspace ownership. Familiar names, ports, or missing labels never authorize adoption or deletion. Ownership checks include networks and volumes even though `status` currently lists only containers. Isolation does not partition external databases, queues, buckets, browser cookies, or cloud resources; applications own those concerns.

## Command workflows

| Command | Workflow and mutation boundary |
| --- | --- |
| `init` | Discover Git, validate existing configuration or propose minimal SDK-selected Compose files, resolve the target, show changes, confirm, then persist configuration/registration. Requires a terminal; does not infer endpoints/bindings or create Docker resources. |
| `plan`, `doctor` | Load confirmed identity, configuration, and relevant observations; validate and plan without writes, allocation claims, or rendered output. `doctor` checks the supported subset, not general security. |
| `up` | Lock the workspace/target, load and validate, observe ownership, plan and allocate, render selected container files, apply through Compose, verify publications, and remember confirmed mappings. |
| `start`, `stop`, `down` | Reconstruct existing resources through the SDK, verify ownership, and perform the lifecycle operation without reloading current Compose/manifest files. |
| `status`, `endpoints` | Query owned resources without changing files or Docker state. Unregistered workspaces fail rather than registering implicitly. |
| `ls` | Read the registry and live containers for the selected Docker target; list stacks with their worktree paths. Does not load manifests, discover Git, or acquire locks. |
| `run` | Validate the selected service, observe referenced host endpoints, prepare native bindings/files, then launch the foreground child without holding workspace/registry locks. |

`up SERVICE...` uses Compose's dependency closure. Services referenced by bindings must be selected or declared in `depends_on`; bindings do not invent readiness dependencies. Detached startup waits up to two minutes for Compose running/healthy readiness. Foreground startup streams logs and holds the workspace mutation lock until the session ends.

`start` preserves existing container configuration and generated mounts; use `up` to apply changes. Docker-assigned ports may change after restart. `stop` retains resources. `down` removes owned containers/networks but retains volumes; `--volumes` additionally removes verified managed named and attached anonymous volumes using Engine semantics. Unattached anonymous volumes are not swept. Images, external resources, bind-mounted files, generated files, and workspace registration remain.

Compose recreation prompts are never automatically approved, orphans are not automatically removed, and failed startup does not roll back volumes. Reinspect after partial failure and retry after correction; do not assume that nothing was created.

## Port allocation and locking

Prefer Docker-assigned ports whenever consumers can wait for discovery. Container bindings or file inputs requiring a host port/URL before startup need coordinated assignments. Native bindings consume already-running publications and do not allocate ports.

Reuse verified mappings first, then available remembered preferences, then OS-selected ephemeral candidates. Probe conservatively for loopback and wildcard conflicts. A probe or lease is not a socket reservation against unrelated programs; Docker's successful bind is authoritative.

Per-port OS-backed file locks coordinate concurrent Trocker processes across local daemon identities sharing the same user state directory. Process exit releases claims; stale lock files do not require PID-based adoption or deletion. Limits are 32 candidate probes per publication and three startup attempts. Retry only recognized Engine bind failures corroborated by owned-container failed-start state. Refresh observations, assignments, bindings, and rendered files on retry; never terminate a competing listener or retry unrelated errors as bind conflicts.

Lock scopes are deliberately separate:

- Initialization uses a Git-administrative-path workspace lock, then a short registry lock; prompting and Docker discovery happen before locking.
- Workspace/target mutation locks serialize changes to a stack.
- Per-port leases span allocation/startup verification or the foreground session.
- Registry locks protect short metadata updates, never builds, pulls, readiness waits, or child lifetimes.
- Generated-file consumer locks protect only generation comparison/publication.

Only verified publications become remembered preferences. Plans and read-only commands neither claim ports nor present remembered/proposed addresses as live endpoints.

## Persistence

| Platform | State directory |
| --- | --- |
| Linux | `$XDG_STATE_HOME/trocker`, otherwise `~/.local/state/trocker` |
| macOS | `~/Library/Application Support/trocker` |
| Windows | `%LOCALAPPDATA%\Trocker` |

Versioned JSON and atomic writes hold minimal workspace/target associations and port preferences. OS locks coordinate processes. Unix state is private; Windows registry state relies on the user profile's inherited ACL. Corrupt or unsupported state is an error, not an empty registry.

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

Diagnostics, prompts, progress, and foreground container logs go to stderr; results go to stdout. Compose's SDK renderer owns resource progress: terminal rendering interactively and plain rendering for pipes/CI, `NO_COLOR`, and `TERM=dumb`. Trocker context is placed before or after live progress, not interleaved. Build-step logs remain suppressed and known SDK failures are classified into safe diagnostics.

Errors have a concise summary and optional action/context. Plans and diagnostics can show binding names and references, never resolved environment values, rendered content, raw secret-bearing configuration, or authentication data. Preserve internal causes without exposing them indiscriminately.

Exit status is 0 for success, 1 for configuration/runtime failures, and 2 for CLI usage errors. Native child exit codes, including 2, propagate without an extra Trocker diagnostic. Signal exits use 128 plus the signal number; explicit child exit codes take precedence, while signal-canceled execution does not become successful merely because the child returned zero.

`endpoints --json` emits schema version 1 with `workspace_id`, `target_id`, and alias-sorted `endpoints`. Each endpoint contains `name`, `service`, `container_port`, `scheme`, `state`, and `addresses` with container/host address objects (`host`, `port`, and `url` except for TCP). States are `published`, `stopped`, or `missing`; stopped/missing publications have null host addresses. Published means an observed running mapping, not application readiness. Ambiguous or replicated mappings and ownership conflicts fail rather than selecting arbitrarily. An unreachable daemon is an error, not a fabricated missing result. JSON failures leave stdout empty.

## Validation and limits

Tests protect Trocker's decisions and safety contracts: pure transformations, identity, read-only behavior, ownership, retention, concurrent allocation/publication, secret-safe failures, and interrupted-operation recovery. Compose loader fixtures live in `internal/compose/testdata/contracts`; integration tests use real disposable resources, never a developer's existing stack. Do not mock SDK calls merely to verify their sequence or retest SDK internals.

Current limits include retained-resource listing, explicit recovery/forgetting, intentional sharing policies, remote targets, UDP/IPv6 publications, Windows native execution/generated-file delivery, and generated-file cleanup. These are not implicit capabilities. There is no daemon, generic plugin/workflow framework, cloud-resource manager, gateway, automatic TLS, or application-specific OAuth/CORS rewriting.
