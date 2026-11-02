# Initial implementation contracts

**Status:** implementation baseline. Minimal interactive `init`, configuration validation, workspace registration, and local target resolution are implemented. Read-only container observations, `plan`/`doctor`, and the initial `up` subset are implemented. Existing-resource `start`, `stop`, and `down` are implemented; native `run`, including optional generated configuration files, is implemented on Linux/macOS. This document settles step 1 of the [implementation plan](implementation-plan.md); the [architecture](architecture.md) remains the reference for ownership and safety. Older report examples illustrate broader future capabilities, not the initial schema.

## 1. Manifest version 1

The workspace root contains `trocker.yaml`. Commands invoked below that root use the same manifest. There is no recursive search for arbitrary configuration files and no automatic manifest creation outside `init`.

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
    native:
      environment:
        INTERNAL_API_URL: "{{ endpoints.api.host.url }}"
```

### Structure

- The manifest is one YAML document decoded into typed Go structs with strict field and duplicate-key checking. Unknown fields, duplicate keys, incompatible value shapes, and additional documents are errors. YAML aliases and merge keys use the library's normal semantics; Trocker does not implement a separate YAML dialect.
- Null values use the library's zero-value/omission behavior, then undergo the same semantic validation as missing values. Required fields must still pass validation; optional sections may be null. Scalar conversions follow the YAML library (for example, numeric and boolean environment values become strings). Quote values when their exact spelling matters.
- `version` is required and must be integer `1`. Unsupported versions fail before any effects.
- `compose` is a required, nonempty ordered list of nonempty file paths. Scalar shorthand is not supported. Paths resolve relative to the manifest directory, not the invocation directory. Absolute paths are allowed; environment expansion, globbing, and shell expansion in these paths are not performed. Referenced files must exist and be readable.
- `endpoints` and `bindings` are optional mappings, defaulting to empty. Empty mappings are valid.
- Endpoint aliases match `[a-z][a-z0-9_]*`. This deliberately keeps dotted binding references unambiguous. Service names remain Compose service names, not endpoint aliases.
- Each endpoint requires exactly `service`, `port`, and `scheme`. `service` is a nonempty string; `port` is an integer from 1 through 65535; `scheme` is one of `http`, `https`, `ws`, `wss`, `tcp`.
- Version 1 endpoints use TCP transport only. `https` and `wss` describe an existing TLS listener; Trocker does not configure TLS. UDP endpoint support requires a later explicit contract.
- Each `bindings` key identifies an actual Compose service. Its allowed fields are `environment`, `files`, and `native`. `native` allows `environment` and optional `files`. Environment maps use names matching `[A-Za-z_][A-Za-z0-9_]*` and decode to string values, including empty strings. Numeric and boolean scalars are accepted through the library's string conversion; nested maps/lists are invalid.
- Top-level `files`, build arguments, hooks, profile selection, target settings, port preferences, and sharing policies are not version 1 manifest fields. Reject them rather than silently ignoring them.

Delegate the contents and merging of Compose files, includes, interpolation, `.env` semantics, and Compose validation to the SDK. Use the manifest directory as the project working directory. The initial CLI does not introduce profile flags; SDK environment/profile selection remains subject to validation of the resulting active project. References to inactive or missing services are errors.

An endpoint's container port must match a TCP `ports[].target` or `expose` declaration on its active service. A missing declaration or transport mismatch is a configuration error, not permission to rewrite the manifest. This validation does not prove a listener exists.

## 2. Binding language and precedence

Binding values are literal strings with zero or more `{{ reference }}` substitutions. Whitespace immediately inside delimiters is allowed. Supported references are:

- `workspace.id`
- `endpoints.<alias>.<container|host>.<host|port|url>`
- `files.<identifier>.path`, only in the declaring consumer's environment (native host path or container target)

There are no functions, operators, indexing, conditionals, recursive substitutions, shell execution, or arbitrary environment lookups. Unmatched delimiters, empty expressions, and unsupported references are errors. Literal brace characters outside delimiter pairs are unchanged; literal `{{` is not supported in this initial grammar. Values produced by substitution are never reparsed.

Ports render as decimal strings. Hosts are unbracketed address components; URL serialization brackets IPv6 addresses as required. URLs contain scheme, host, and explicit port, with no credentials, path, query, or trailing slash. `tcp` supports `host` and `port`, but not `url`. Separate secret inputs supply credentials.

`bindings.<service>.environment` applies only to that service's containers. `bindings.<service>.native.environment` applies only to `run SERVICE`; it is not merged with or inferred from container bindings. Native children inherit the invoking process environment, then receive native bindings. They do not inherit the service's Compose environment automatically. Reject native references to container addresses in the first implementation rather than claiming host-to-container DNS reachability.

Container bindings override the resolved Compose environment. Native bindings override inherited environment. Trocker then supplies the registered `TROCKER_WORKSPACE_ID` to all managed containers and native children, even services with no bindings. Explicit Trocker bindings for this reserved variable may only be exactly `{{ workspace.id }}`. A conflicting value explicitly supplied by Compose is an error; a stale inherited native-process value is replaced.

Container addresses use the endpoint service DNS name and declared container port. Container consumers must share a supported Compose network with the endpoint service. Host addresses use the observed local loopback publication. Host addresses do not promise remote access, cookie isolation, TLS termination, or a gateway.

Only `container` and `host` address representations are supported. The redundant, unreleased `public` representation has been removed from binding references and endpoint JSON; use `host` instead. This cleanup updates the initial version-1 contract without retaining an alias.

The configuration loader accepts syntactically valid host references even when their values require runtime observations. Planning distinguishes unresolved runtime values from invalid references.

## 3. Initial runtime support and isolation

The first mutation slice supports a local Docker daemon running Linux containers with ordinary Compose bridge networking and TCP port publications. Docker Desktop on macOS/Windows is considered local when published ports are accessible on the invoking host. Reject remote SSH/TCP targets and unsupported container platforms before mutation. Loader-only operations do not require a daemon.

Use Docker's normal configuration/context/environment resolution through the SDK integration. Do not introduce a second target-selection algorithm. Record the Engine-reported daemon identity in the workspace target association, not merely the context name or socket path. If that identity cannot be obtained, fail. Different context aliases resolving to the same daemon share one target association. A replaced daemon is a different target requiring explicit initialization/confirmation; queries and `up` must not silently retarget a workspace. SDK API details must be verified when selecting dependencies.

Names derive from the persisted UUID: use `trocker-` followed by the lowercase UUID without hyphens for the Compose project name. Apply ownership label `io.trocker.workspace-id` with the canonical UUID to managed resources where supported, alongside Compose project labels. Missing or conflicting ownership blocks mutation; labels are safety metadata, not authentication against a hostile Docker client.

The initial planner:

- Uses Compose project-scoped names and removes explicit `container_name` overrides in the transformed model.
- Uses workspace-specific tags for locally built service images; does not retag externally supplied images or disable build cache reuse.
- Replaces TCP publications with loopback-only host ports. Docker assigns them unless a container binding requires pre-start allocation. Container ports remain unchanged. Identical duplicate publications collapse to one; conflicting/ambiguous declarations fail.
- Rejects UDP publications, host networking, nonstandard network modes, external or explicitly named volumes/networks, and writable bind mounts. Read-only bind mounts are supported on the local target. Later support requires an explicit sharing policy, not silent adoption.
- Preserves managed named volumes unless `down --volumes` is explicitly requested.

The initial implementation also rejects explicit anonymous mounts, deploy/replication settings, hooks/providers, privileged/device/namespace access, secrets/configs/models, and advanced build access/tags. Built service names must be lowercase. Image-defined anonymous volumes are inherited on recreation through Compose; explicitly declared managed named volumes are the supported persistent-data configuration. Compose prompts that could approve destructive changes return errors rather than being auto-confirmed.

`up` supports container and host references in container bindings. Host `port` and `url` references request coordinated publications; `host` alone needs no allocation. Endpoint aliases share one publication, and self-advertised URLs work. All addresses are prepared before SDK startup, so mutual URL references need no inferred ordering. Compose dependency cycles remain invalid and are rejected by the SDK during loading. Service selection uses the SDK's dependency closure; a binding's endpoint service must be selected or declared in `depends_on`. Detached `up` waits up to two minutes for SDK running/healthy readiness; foreground mode streams application logs to stderr. `run` consumes observed host addresses from an already-running stack without starting dependencies implicitly. Trocker does not infer readiness dependencies from advertised URLs; declare required ordering/health conditions in Compose.

## 4. Registration, moves, and recovery

Registration requires a Git worktree and an explicitly selected, reachable supported target. The main checkout and linked worktrees are both workspaces. A branch name, path, port, or target name is never the workspace UUID.

`init` proposes a complete manifest or validates an existing one, shows the selected target and registration actions, and requires confirmation before writes. The current proposal contains SDK-selected Compose files with no inferred endpoints/bindings; users configure those explicitly. Noninteractive execution without a confirmation mechanism fails without writes; an explicit noninteractive confirmation flag is a later CLI addition. Discovery may suggest services and declared ports but must ask for unknown schemes and application binding semantics. It never rewrites application configuration.

Registration generates a cryptographically random UUID once. Repeated initialization reuses it. Publish the same UUID as `trocker-workspace-id` in the per-worktree Git administrative directory after committing the registry. This marker detects a lost registry or an unsupported move/copy of the entire Git directory; it never authorizes adopting resources. A registry entry without a marker can complete marker publication on a confirmed retry; a marker without its registry entry requires recovery. Record canonical checkout and Git administrative paths as association evidence, with distinct associations for independent clones and linked worktrees. Paths are association locators, not identity derivation inputs.

Branch switches preserve identity. Initially support a linked-worktree move when its recorded Git administrative directory remains unchanged, its old checkout no longer exists, and Git reports exactly one new checkout for that association. Persist the relocation only during confirmed `init`. Read-only commands report relocation required without updating metadata. Moving the main clone or the common Git administrative directory requires explicit recovery and is not automatically supported.

If the association is ambiguous, the registry is corrupt/unsupported, or expected ownership is missing, stop with a recovery diagnostic. Do not create a replacement identity to make an error disappear. Recovery and forgetting commands are deferred: report that automatic recovery is unsupported, preserve metadata, and do not instruct users to delete the registry as a repair. Loss of registry data never authorizes adopting existing resources.

Manifest and registry updates cannot form a single filesystem transaction. Validate and confirm first, write the approved manifest atomically before registration, and make retries accept that valid existing manifest. Never claim initialization succeeded unless both necessary writes completed; a failed registration leaves an actionable error and no Docker resources.

`plan`, `status`, `endpoints`, and `doctor` never register a workspace. For an unregistered workspace, they return a registration-required error; configuration validation helpers remain independently testable. This avoids inventing preview UUIDs or persisting identities during observation.

`start`, `stop`, and `down` reconstruct existing resources through Compose without loading current configuration. They require verified registration and ownership and serialize workspace mutations. `start` preserves container IDs and HostConfig publications; Docker may reallocate a dynamically assigned host port on restart. Fixed bindings are never silently changed to resolve conflicts. `stop` retains containers and volumes; `down` retains volumes unless explicitly passed `--volumes`. Teardown checks all project-labelled containers, including those omitted by ordinary `Ps`, and validates retained resource ownership before removal. Engine decides which attached volumes are anonymous; named external volumes and bind-mounted files are not deletion targets. Detached/unattached anonymous volumes are not swept.

Every lifecycle operation, including `down --volumes`, preserves registration. New target associations require confirmation through `init` and do not change workspace UUIDs.

## 5. Output and failure contracts

Retain exit status 2 for CLI usage errors, 1 for configuration, registration, ownership, unsupported-feature, I/O, or runtime failures, and 0 for successful commands. A native child's exit status propagates as a child result, even when it is 2; it is not reclassified as usage. On Linux/macOS, signal exits map to 128 + signal number. Explicit child exit codes take precedence; a signal-canceled launch whose child exits successfully still returns 128 + the forwarded signal. Launch/I/O failures are not child exits.

Native children retain stdin/stdout/stderr directly; Trocker emits no success summary or extra diagnostic for a normal child exit. Terminal foreground ownership and settings are restored after execution.

Human diagnostics go to stderr. Requested results go to stdout. Failures do not emit a success-shaped result. Preserve underlying error causes internally without making raw SDK errors or secret-bearing models the public output.

Only `endpoints --json` is currently an advertised JSON flag. Its initial success schema is:

```json
{
  "schema_version": 1,
  "workspace_id": "e353936d-8f28-4b8e-a7a2-a629702886bd",
  "target_id": "opaque-engine-identity",
  "endpoints": [
    {
      "name": "api",
      "service": "backend",
      "container_port": 8000,
      "scheme": "http",
      "state": "published",
      "addresses": {
        "container": {"host": "backend", "port": 8000, "url": "http://backend:8000"},
        "host": {"host": "127.0.0.1", "port": 24173, "url": "http://127.0.0.1:24173"}
      }
    }
  ]
}
```

Sort endpoint entries by alias. `state` is `published`, `stopped`, or `missing`. `published` means a running owned container has an observed mapping; it is not an HTTP/readiness guarantee. For stopped/missing services, host addresses are null. A running service without a matching publication is also `missing` in endpoint output (the publication is missing, not necessarily the container). Wildcard IPv4/IPv6 bindings become `127.0.0.1`/`::1` for the supported local target. The container address remains a derived configuration address, not proof of connectivity. Omit `url` for `tcp`. Ambiguous mappings or conflicting ownership fail rather than choosing an arbitrary container. Replicated endpoint services with multiple mappings are initially unsupported.

No planned addresses or remembered preferences appear as observed mappings. An unreachable daemon is an error, not a fabricated `missing` result. JSON failures use empty stdout and a stderr diagnostic; structured JSON errors and JSON flags on other commands are deferred.

Plans and diagnostics show binding names and references, never resolved environment values. Never dump inherited environments, Compose environment maps, authentication configuration, or raw manifest contents. Validation errors identify paths and categories without echoing rejected values. SDK progress/errors must be reviewed for secret exposure before being surfaced.

## 6. Acceptance cases

Manifest examples and expected outcomes live in [fixtures/contracts](fixtures/contracts/README.md). They are exercised by `internal/compose/load_test.go` using the real, offline SDK loader. Structural YAML errors report a safe location when available; semantic errors report a field path. Initialization, container observations, planning, startup, and existing-resource lifecycle operations are implemented; foreground native execution is implemented on Linux/macOS.

| Case | Expected outcome |
| --- | --- |
| Valid minimal/versioned manifest | Loads without Docker or file mutation. |
| Wrong version, duplicate/unknown fields, invalid reference | Configuration error with a semantic field path or safe YAML location/category, not secret values. |
| Referenced endpoint port absent from Compose declarations | Configuration error; neither file is rewritten. |
| Valid host binding before initial `up` | Read-only plan reports allocation requirements; up coordinates ports and verifies observed mappings. |
| Competing listener takes a pending port | Replan bindings and retry only a corroborated Engine bind failure, at most three startup attempts; never stop the listener. |
| Process dies holding a port claim | OS releases its file lock; retry uses observed resources, not stale lock files or PIDs. |
| Declined initialization | No manifest, registry, or Docker mutation. |
| Repeated initialization / branch switch | Same UUID and project name. |
| Two clones / two linked worktrees | Distinct UUIDs and project names. |
| Supported linked-worktree move | Read-only commands report relocation; confirmed `init` preserves UUID and updates association. |
| Ambiguous move / corrupt registry | Failure; no resetting, adoption, or deletion. |
| Context aliases for one daemon | Same target association. |
| Unreachable, remote, or replaced target | No fallback or silent reassociation; actionable failure. |
| Unregistered read-only command | Registration-required failure; no new UUID or state. |
| Foreign/missing ownership on existing resource | Mutation blocked. |
| Stopped endpoint with old port preference | Null host addresses, never a live-looking old URL. |
| `down --volumes` | Managed volumes removed only after checks; UUID and external resources preserved. |
| Secret in invalid configuration or SDK failure | No secret in stdout, stderr, or persisted metadata. |

## Native execution contract

`run SERVICE -- COMMAND [ARG...]` validates the active service and uses only `bindings.<service>.native.environment` and `.native.files`. It preserves the invoking directory and argv, inherits the process environment, overlays native bindings, then overrides `TROCKER_WORKSPACE_ID` with the verified registration. It never imports Compose environment values. PATH lookup uses the parent's PATH via `os/exec`. Only referenced endpoint aliases are observed; missing/stopped publications or ownership conflicts prevent launch. Literal/UUID-only bindings require no Docker connection. No preferences, Docker resources, or allocation claims are changed. Files are generated only when explicitly declared; no locks remain held during the child lifetime. Observed addresses are a snapshot; dependency changes require restarting the child.

Linux/macOS use an owned process group with real terminal handoff where applicable. SIGINT/SIGTERM received by Trocker are forwarded; programmatic cancellation sends SIGTERM. After five seconds, cancellation escalates to SIGKILL. Normal leader exit also cleans remaining group members. Exit observation precedes reaping so PID reuse cannot authorize signalling an unrelated group. Escaped sessions/groups and privilege elevation are not supported process-tree management. Windows execution fails explicitly until a tested process-tree implementation exists. Detached supervision and allocation of the native application's own listening ports are not provided.

## Native generated-file contract

`bindings.<service>.native.files` maps restricted file identifiers (`[a-z][a-z0-9_]*`) to `template`, `output`, `format`, and optional string `values`. `template` is a relative regular file confined to the manifest directory tree. `output` is a basename, unique case-insensitively within the consumer; paths, backslashes, colons, and traversal are rejected. `format` is explicitly `json` or `text`. Value keys match environment-name syntax and values use the existing binding language, restricted to identity/host references. File-to-file references are not supported. Every native file needs an explicit `files.<identifier>.path` reference in that consumer's native environment.

Templates use standard `text/template` with `missingkey=error`, ordinary built-ins, and one custom `json` encoder. Only explicitly declared string values are exposed. There are no ambient environment, filesystem, network, or execution helpers. Each template and rendered output is capped at 1 MiB; this is not a template sandbox or execution-time guarantee. JSON is validated before any publication. Errors name the file and safe category, never raw execution errors or content.

`init`, `plan`, and `doctor` validate inputs/syntax without rendering. `run` observes required endpoints, renders/validates the complete set, publishes private immutable files, resolves environment paths, then launches unchanged argv. Identical current output is reused. Changes create a new generation under private workspace/target/service state; local-only inputs stay offline. Publication holds only a short consumer lock, not the registry/workspace lock. Directories are `0700`, files and metadata `0600`; anchored filesystem operations reject symlink escapes and unsafe existing state. Failed rendering changes no output; write/rename failures preserve previous generations. Complete or uncertain generations survive launch failure, cancellation, and parent crashes.

No automatic generated-file cleanup is implemented, including on `down --volumes`; old secrets can remain in private state. Container delivery uses the separate contract below. See [the detailed contract and container proposal](generated-files-proposal.md) and `examples/native-config`.

## Container generated-file contract

`bindings.<service>.files` uses the same template/output/format/values fields, plus required `target`: a clean absolute Linux container file path. Container values support workspace identity and container/host endpoint references with the same service-selection/network rules as container environment bindings. File paths resolve only in the declaring consumer's environment, to its container target, never the host source. Container files can omit environment-path references when the application already knows its explicit target.

Reject special filesystem/Docker hostname/DNS targets and paths overlapping each other or any declared Compose volume, tmpfs, config, or secret mount. `init`/`plan`/`doctor` check declarations and template syntax without rendering. `up` renders all selected container sets before publishing or calling Compose. Host port/URL inputs join allocation requirements, and every bind-conflict retry renders with the new allocation. Native files are not rendered by `up`.

Delivery uses the pinned Compose SDK's file-backed configs, which create individual read-only bind mounts. Each host file is `0444` beneath private `0700` directories; metadata remains `0600`. This grants any container UID read access without making the host directory traversable by other ordinary users. It is not per-UID secrecy inside a container. The daemon must have access to the private state tree; no permission weakening or fallback is attempted. Linux/macOS hosts are supported; Windows hosts are rejected before publication pending ACL validation. Arbitrary Compose configs/secrets remain unsupported inputs.

The container scope is distinct from native state. Config reference names contain opaque generation IDs so Compose's service hash changes with rendered bytes without exposing content digests. Unchanged current content reuses the generation. `start` preserves existing mounts; `up` applies edits. Startup failure, stopped containers, cancellation, and `down --volumes` never authorize generation deletion. Automatic cleanup remains deferred.

## 7. Deliberately deferred contracts

Current allocation uses OS-selected ephemeral candidates (no configurable range), up to 32 candidate probes per required publication and three startup attempts. Existing verified publications take precedence over remembered preferences. Only confirmed mappings are persisted in `port_preferences` in the version-1 registry; foreground services that have already exited do not acquire invented live mappings. Claims are OS-backed per-port locks shared by all local targets using the same state directory. They are held through startup/verification, or the foreground session, without holding the registry lock. These claims are operation-owned, not PID-only records, and vanish on process death without metadata repair. Socket probes are advisory; loopback and wildcard overlap are checked conservatively, and Docker's actual binding remains authoritative. Unknown Engine errors and non-bind failures are not retried.

Later slices must specify their own additions before implementation: UDP/IPv6 publications and remote targets, intentional sharing policies, Windows native process-tree behavior, native listening-port allocation, recovery/forgetting interfaces, Windows-host generated-file permissions, and safe generated-file cleanup. These are not implicit version 1 capabilities. Additive schema changes must remain explicit and tested; incompatible changes require a new manifest version.
