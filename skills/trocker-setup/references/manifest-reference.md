# `trocker.yaml` version 1 reference

`trocker.yaml` is a strict, small schema. It is not a Compose file and it must not contain Compose overrides. Unknown fields, duplicate keys, extra YAML documents, and wrong value shapes are errors.

## Minimal manifest

```yaml
version: 1
compose:
  - compose.yaml
read_write_paths: []
endpoints: {}
bindings: {}
```

`read_write_paths`, `endpoints`, and `bindings` are optional. Empty values are valid and are what `trocker init` normally generates before application-specific configuration is added.

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

External host paths, such as a Docker socket or a home-directory cache, cannot be declared here. They are separate unsupported features.

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
- Privileged services, devices, device cgroup rules, added capabilities, PID/IPC/UTS/cgroup namespaces, socket use, `volumes_from`, and `external_links`.
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
