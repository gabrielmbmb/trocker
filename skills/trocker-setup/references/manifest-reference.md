# `trocker.yaml` version 1 reference

`trocker.yaml` is a strict, small schema. It is not a Compose file and it must not contain Compose overrides. Unknown fields, duplicate keys, extra YAML documents, and wrong value shapes are errors.

## Minimal manifest

```yaml
version: 1
compose:
  - compose.yaml
profiles: []
stable_ports: false
read_write_paths: []
shared_write_paths: []
build_contexts: []
docker_socket_services: []
host_access: {}
endpoints: {}
bindings: {}
```

`profiles`, `stable_ports`, `stable_port_range`, `read_write_paths`, `shared_write_paths`, `build_contexts`, `docker_socket_services`, `host_access`, `endpoints`, and `bindings` are optional. Empty values are valid and are what `trocker init` normally generates before application-specific configuration is added.

A typical configured manifest:

```yaml
version: 1
compose:
  - compose.yaml
  - compose.override.yaml
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
        API_URL: "{{ endpoints.api.host.url }}"
```

## Top-level fields

### `version`

Required. Must be the integer `1`. Unsupported versions fail before any effects.

### `compose`

Required, nonempty, ordered list of nonempty file paths.

- Paths resolve relative to the directory containing `trocker.yaml`. Absolute paths are allowed but relative paths are more portable.
- Environment expansion, globbing, and shell expansion are not performed in these paths.
- Referenced files must exist and be readable.
- List the top-level Compose files in merge order. Do not list files that are only reached through `include` or `extends.file`.
- Scalar shorthand is not supported; use a YAML list.

The Compose project name comes from Trocker's workspace identity. Do not try to set or preserve a Compose `name` through this field.

### `profiles`

Optional ordered list of Compose profiles to activate.

```yaml
profiles:
  - workos-local
  - n8n
```

- A nonempty list replaces `COMPOSE_PROFILES` from the environment or project `.env`. This keeps profile-gated services, endpoints, and bindings deterministic across shells.
- `*` activates every profile. It must be the profile name, not a glob.
- Names use `[A-Za-z0-9][A-Za-z0-9_.-]*`; empty names, duplicates, and invalid names are rejected.
- An empty list is the same as omitting the field: the environment fallback remains in effect.
- An explicit profile that no Compose service declares is reported with the profiles that are available.
- `--profile` on `plan`, `doctor`, `up`, `run`, `status`, or `endpoints` overrides the manifest for that command. `plan` and `doctor` report the active profiles and service count.
- Commands that operate on existing resources (`start`, `stop`, `down`, `release`, `ls`, `ps`, `logs`, `exec`) do not load Compose and do not take a profile selection.

### `stable_ports`

Optional boolean, false by default. When true, Trocker assigns one stable host port to every workspace TCP publication and persists it.

```yaml
stable_ports: true
```

- No host-port values are declared in `trocker.yaml`. Trocker derives the publications from Compose `ports` and endpoint declarations backed by `expose`.
- A new assignment prefers the current publication, a remembered preference, or the concrete host port Compose already resolved. If none is available, Trocker takes the first free ports of `stable_port_range`, keeping one workspace's ports contiguous when it can.
- A current or remembered port inside the OS ephemeral range is not adopted, so enabling `stable_ports` on a running stack moves those publications once. An explicit Compose host port is kept even there.
- Once assigned, the value is authoritative: `up` claims it exactly and fails on conflict. It never silently falls back to an ephemeral port.
- Assignments survive `down`/`up` and are removed by `trocker release`. Disabling `stable_ports` or removing a publication from Compose does not delete assignments; stale assignments remain until the workspace is released.
- `plan` shows assigned values but does not claim them. Missing assignments are reported as pending and are created by `init` or the next `up`.
- `trocker ports` shows the stable mapping; `trocker ports --json` emits `workspace_id`, `target_id`, and `{service, container_port, host_port}` entries. Scripts should read this instead of a per-worktree `.env` for host ports.
- Use endpoint bindings for URLs that contain those ports. The host-port values do not need to be interpolated into Compose.
- Compose host ports are only seeds. If the value is already assigned to another workspace or is unavailable, Trocker chooses a port from `stable_port_range` instead. Consumers must read `trocker ports`, not assume a literal Compose value.
- There is no per-port reallocation command in this version. If a fixed port becomes permanently unavailable, stop the conflicting process or release and re-register the workspace.

### `stable_port_range`

Optional inclusive `first-last` range for new stable assignments. Valid only with `stable_ports: true`; defaults to `20000-32767`.

```yaml
stable_ports: true
stable_port_range: 30000-32767
```

- Both ports are from 1024 through 65535, and `first` is not above `last`.
- `init`, `plan`, and `doctor` reject a range, declared or default, that overlaps the host's ephemeral port range (read from the kernel on Linux and macOS, 49152-65535 elsewhere). The OS hands those ports to programs listening on port zero, which would make a stable claim fail later.
- Declare a range when another tool on the machine already uses part of the default, for example a legacy per-worktree port scheme. Ports in use when Trocker assigns are skipped, but a stopped tool's ports are not visible then.
- Changing the range affects new assignments only; existing assignments stay where they are.
- If the range has too few free ports, assignment fails and names the range.

### `read_write_paths`

Optional list of host paths that containers may modify through writable bind mounts. Read-only bind mounts need no declaration.

```yaml
read_write_paths:
  - .
  - packages/api
```

- Each entry is nonempty. Relative paths resolve against the directory containing `trocker.yaml`; the resolved path must be that directory or inside it.
- A writable bind mount is accepted when its source equals a declared path or is nested under it. Siblings that merely share a name prefix are not covered.
- Containment is lexical. Trocker does not follow symlinks, so do not route a mount source through a link that leaves the checkout.
- Undeclared writable mounts fail before mutation, naming the path to declare.
- `plan` and `doctor` list every writable mount, because containers can modify those host paths once `up` runs.

External host paths cannot be declared here; use `shared_write_paths`. Docker sockets use `docker_socket_services` instead.

### `shared_write_paths`

Optional list of host paths outside the checkout that containers may modify. These are machine-wide paths, such as a credentials cache, that other tools and every workspace also use.

```yaml
shared_write_paths:
  - ~/.aws/sso/cache
```

- A leading `~/` expands to the home directory. Entries must otherwise be absolute; relative paths, `~user/...`, and environment expansion are rejected.
- A path inside the checkout is rejected: use `read_write_paths`. A path that contains the checkout is also rejected.
- The filesystem root and the home directory itself are rejected, and duplicates fail.
- A writable bind mount outside the checkout is accepted when its source equals a declared path or is nested under it. Read-only external mounts need no declaration.
- Mounts authorized this way are planned with `create_host_path: false`, so a missing directory fails instead of being created by Docker.
- Trocker never creates, labels, or deletes these paths; teardown and `release` leave them untouched.
- The declaration never authorizes the Docker API socket; that stays under `docker_socket_services`.
- `plan` and `doctor` list each one as `Shared writable mount: <host path> -> <container path>` and state how many mounts are shared.

### `build_contexts`

Optional list of host paths that Compose `build.additional_contexts` entries may read. `build.context` and `build.dockerfile` need no declaration.

```yaml
build_contexts:
  - packages
```

- Each entry is nonempty. Relative paths resolve against the directory containing `trocker.yaml`; the resolved path must be that directory or inside it.
- An additional context is accepted when its source equals a declared path or is nested under it. Matching is lexical and component-wise, like `read_write_paths`.
- Values that are not local paths are rejected: `service:<name>` references and remote contexts (`https://`, `git@`, `docker-image://`, `ssh://`) are unsupported. Errors name the context but never repeat the value, which can carry credentials.
- Undeclared contexts, and paths outside the checkout, fail before mutation.
- `plan` and `doctor` list each context as `Build context: <name> -> <host path>`.
- A declaration authorizes reads at image build time only. It does not authorize a writable mount; use `read_write_paths` for that.

### `docker_socket_services`

Optional list of active Compose service names that may access the Docker API socket.

```yaml
docker_socket_services:
  - localstack
```

- A listed service is accepted when it mounts a Docker socket or sets Compose `use_api_socket: true`. A Docker socket is a bind source named `docker.sock`, or a mount of the conventional socket paths (`/var/run/docker.sock`, `/run/docker.sock`) or a parent directory of those. Detection is lexical; a socket reached through a differently named path or a symlink is not recognized.
- Read-only socket mounts need the same declaration; mount mode does not prevent connecting to the socket.
- The socket path does not belong in `read_write_paths`, and this list does not authorize any other host path.
- Undeclared access fails before mutation and names the service to add.
- Docker API access is host-level: the service can control the daemon and resources outside this workspace. Add entries only after confirming the need.

External host paths other than Docker sockets still cannot be declared.

### `host_access`

Optional mapping from active Compose service name to host-level access grants.

```yaml
host_access:
  datadog-agent:
    pid: true
    ipc: true
    capabilities:
      - SYS_PTRACE
    devices:
      - source: /dev/kmsg
        permissions: rw
```

- Each grant must contain at least one of `pid`, `ipc`, `capabilities`, or `devices`.
- `pid: true` permits `pid: host`. Other `pid` modes are unsupported.
- `ipc: true` permits `ipc: host`. `ipc: private` is allowed without a grant; other IPC modes are unsupported.
- `capabilities` is a case-insensitive allow-list for Compose `cap_add`; a `CAP_` prefix is ignored. `ALL` is rejected; list individual capabilities.
- `devices` matches a Compose device by source and exact permissions. Permissions use `r`, `w`, and `m`; order does not matter. The manifest entry must state permissions explicitly; approving a Compose device that omits them requires `rwm`. The container target is not part of the grant. Matching is lexical; symlinks are not resolved.
- Empty grants, empty capability names, invalid permissions, duplicate capabilities/devices, and unknown or inactive services are rejected.
- `privileged`, `device_cgroup_rules`, `uts`, `cgroup`, and other host namespace modes remain unsupported.
- `plan` and `doctor` report each active grant. Host access is not workspace-isolated.

### `endpoints`

Optional mapping from alias to endpoint:

```yaml
endpoints:
  api:
    service: backend
    port: 8000
    scheme: http
```

- Alias: `[a-z][a-z0-9_]*`. Dots are not allowed, so binding references stay unambiguous.
- `service`: nonempty, exact active Compose service name.
- `port`: integer from 1 through 65535. This is the container/listening port, not the published host port.
- `scheme`: `http`, `https`, `ws`, `wss`, or `tcp`.
- The port must be declared as TCP by the service through `ports[].target` or `expose`.
- `https` and `wss` mean the application already listens with TLS. Trocker does not terminate or configure TLS.
- `tcp` endpoints support `host` and `port` components but not `url`.
- The `public` address representation is unsupported; use `host`.
- Several aliases may name one service port; they share a single publication, so declare one alias per scheme a consumer needs (for example `gateway` on `http` and `gateway_ws` on `ws`).

### `bindings`

Optional mapping from Compose service name to consumer configuration. Each service may have:

```yaml
bindings:
  web:
    environment: {}      # container environment overrides
    native:
      environment: {}    # trocker run environment overrides
      files: {}          # generated files for native execution
    files: {}            # generated files delivered to the container
```

All sections are optional. The service key must name an active Compose service. Environment names match `[A-Za-z_][A-Za-z0-9_]*` and values are strings.

Bindings are keyed by service name, not by service definition: a service declared with `extends` does not inherit the extended service's binding, so repeat the entries it needs.

- `environment` overrides that service's Compose runtime environment after Compose resolves its own values.
- `native.environment` has a separate scope. It is applied only by `trocker run SERVICE`, after the invoking process environment. It is not inherited from or merged with `environment`.
- `files` and `native.files` are described below.
- Explicitly setting `TROCKER_WORKSPACE_ID` is reserved: it must be exactly `{{ workspace.id }}`. Otherwise it is an error.
- Trocker normally injects `TROCKER_WORKSPACE_ID` automatically; you usually do not need to declare it.
- Bindings do not create service dependencies, readiness checks, or network links. Declare those in Compose.

## Binding reference language

A binding value is a literal string with zero or more substitutions:

```text
{{ workspace.id }}
{{ endpoints.<alias>.<container|host>.<host|port|url> }}
{{ files.<identifier>.path }}
```

Rules:

- Whitespace immediately inside `{{` and `}}` is allowed.
- There are no functions, operators, indexing, conditionals, recursive substitutions, shell execution, or arbitrary environment lookups.
- Unmatched delimiters, empty expressions, unknown aliases/services/files, and unsupported components are errors.
- Values produced by substitution are not reparsed.
- `files.<identifier>.path` is allowed only in the environment of the consumer that declares that file.
- A `container` reference is not allowed in native bindings.
- A `url` reference is not allowed for a `tcp` endpoint.
- Literal text may surround references, for example `"{{ endpoints.api.container.url }}/v1"`.
- Do not put credentials in bindings.

### Address meanings

| Reference | Meaning |
| --- | --- |
| `endpoints.api.container.host` | Endpoint service name |
| `endpoints.api.container.port` | Declared container port |
| `endpoints.api.container.url` | Scheme + service name + container port |
| `endpoints.api.host.host` | Observed local host address, normally `127.0.0.1` |
| `endpoints.api.host.port` | Observed published host port |
| `endpoints.api.host.url` | Scheme + observed host address + port |

A container consumer using a container address must share a Compose network with the endpoint service. Host addresses are for native processes or browser-facing values; they are not typically reachable from another container's loopback interface.

## Generated files

Generated files are optional and should be added only when the application explicitly reads a configuration file.

```yaml
bindings:
  web:
    files:
      app_config:
        template: .trocker/app.json.tmpl
        output: app.json
        target: /run/app.json
        format: json
        values:
          api_url: "{{ endpoints.api.container.url }}"
          workspace_id: "{{ workspace.id }}"
    environment:
      APP_CONFIG: "{{ files.app_config.path }}"
```

Container file rules:

- `template` is a relative regular file inside the manifest directory tree.
- `output` is a filename, not a path; it must be unique case-insensitively within the consumer.
- `target` is required for container files and must be a clean absolute Linux path outside special filesystems such as `/proc`, `/sys`, and `/dev`.
- `format` is exactly `json` or `text`.
- `values` keys are environment-name-shaped; values use the normal binding language.
- The environment may reference `files.<name>.path`, which resolves to the container target for container files.
- Templates and outputs are limited to 1 MiB.
- `init`, `plan`, and `doctor` validate inputs and template syntax without rendering. `up` renders container files and validates JSON before delivery.
- Old generations are retained; there is no automatic cleanup, including after `down --volumes`.

Native file rules:

- Use `bindings.<service>.native.files`.
- Native files cannot set `target`.
- Each native file must be referenced by `files.<name>.path` in that consumer's `native.environment`.
- `run` renders them after observing dependencies.
- Native values may use `workspace.id` and host endpoint references; they cannot use container references.
- File-to-file references and rendered content from one file into another are not supported.

Read the full template contract in the Trocker repository if debugging generated-file behavior. Do not invent additional template helpers; only `text/template` built-ins and `json` are available.

## Unsupported Compose features

Trocker's first version rejects features it cannot isolate safely. Detect these during analysis and report them; do not silently remove them or rewrite Compose unless the user explicitly asks for a migration.

Rejected by the isolation planner or loader checks:

- Remote Docker targets; only a local Unix-socket or named-pipe target running Linux containers.
- UDP publications. Only TCP publications are supported.
- `network_mode` (including host networking), custom network drivers, external or explicitly named networks, custom network addressing, and driver/IPAM options.
- External volumes, custom volume drivers/options, and explicitly named volumes.
- Anonymous service volumes and writable bind mounts that `read_write_paths` does not declare. Read-only bind mounts are supported.
- Replicated services (`scale` other than 1) and deploy/replication settings.
- Compose `secrets`, `configs`, and `models`.
- Privileged services, device cgroup rules, UTS/cgroup namespaces, `volumes_from`, and `external_links`.
- Host PID/IPC, capabilities, and devices not declared in `host_access`.
- Docker socket access not declared in `docker_socket_services`, whether through a bind mount or Compose `use_api_socket`.
- Pre/post-start/stop hooks, providers, `develop`, and jobs.
- Advanced build access: build tags, network, privileged mode, entitlements, SSH, secrets, and additional contexts.
- Explicit ownership labels that conflict with Trocker's labels, and explicit `TROCKER_WORKSPACE_ID` values that conflict with the registered identity.
- Built service names that are not lowercase.
- Compose dependency cycles and other errors reported by the SDK.

`container_name` is not rejected: Trocker removes it when constructing the isolated project. Existing Compose host publications are replaced by loopback publications; the manifest still declares container ports.

The supported runtime scope is local-host TCP/IPv4 loopback publishing with ordinary Compose bridge networking. WebSocket URLs use `ws`/`wss`; arbitrary raw TCP uses `tcp`. Native execution is unsupported on Windows, and Windows-host generated container-file delivery is rejected pending ACL validation; report either if the user targets Windows.

## Validation and failure behavior

Use Trocker itself as the authority:

```sh
trocker init       # validates the manifest and registers the workspace after confirmation
trocker doctor     # read-only isolation, binding, and ownership checks
trocker plan       # read-only preview using the same checks
```

- `init` validates an existing manifest and does not rewrite it. If the manifest is missing, it proposes the SDK-selected top-level Compose files.
- `init` also resolves the Docker target and registers the workspace. It creates no containers.
- `plan` and `doctor` require a registered workspace and a reachable target. They do not allocate ports, create resources, or rewrite metadata.
- `plan` and `doctor` run the stricter isolation/unsupported-feature checks; use them before declaring the setup complete.
- If the workspace is unregistered, or the selected Docker target is not associated, run `init` and confirm. If the workspace moved ambiguously, its marker is missing, its association is ambiguous, or ownership conflicts, stop and follow the diagnostic. Do not delete the registry or workspace marker.
- If validation fails on a manifest field, fix `trocker.yaml`. If it fails on an unsupported Compose feature or foreign resource, report the blocker.

Errors identify fields and safe categories. Do not paste raw SDK errors, resolved Compose environments, or rejected secret-bearing values into your report.
