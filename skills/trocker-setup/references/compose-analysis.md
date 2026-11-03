# Compose hierarchy and discovery

Trocker delegates Compose loading and merging to the Compose SDK. Your job during setup is to understand the hierarchy well enough to declare the correct top-level files, endpoints, and bindings. Do not reimplement Compose merging in `trocker.yaml`.

## 1. Select the top-level files

`trocker.yaml`'s `compose:` list is the ordered set of files Trocker passes to the Compose loader. Those are the project's top-level files, including explicit overrides. Files pulled in through `include` or `extends.file` are not listed.

Compose normally selects top-level files in this order of precedence:

1. `COMPOSE_FILE` / `COMPOSE_PATH_SEPARATOR` from the environment or a Compose `.env`.
2. The SDK's default discovery: `compose.yaml`, `compose.yml`, `docker-compose.yml`, `docker-compose.yaml`.
3. The first matching optional override: `compose.override.yml`, `compose.override.yaml`, `docker-compose.override.yml`, `docker-compose.override.yaml`.

Default discovery may search parent directories. `trocker init` reports the files it actually selected. Verify that they belong to the intended checkout and stack. If a top-level file is outside the checkout or in a subdirectory with its own `include` paths, check the result carefully: Trocker always uses the manifest directory as the project working directory.

If several default files exist, discovery is ambiguous. Choose explicitly with `COMPOSE_FILE` for the init invocation, or ask the user which stack is intended. Do not let two unrelated root files be merged by accident.

Trocker has no project-directory field: the manifest directory is always Compose's working directory. If the selected top-level file is in a subdirectory and relies on relative includes, build contexts, env files, or mounts resolving from that subdirectory, the project may load differently under Trocker. Verify with `trocker plan`. If the paths break, report the blocker or ask the user to provide a root-level compose wrapper; do not rewrite their Compose paths.

Examples of an explicit selection:

```sh
# Unix-like separator
COMPOSE_FILE=compose.yaml:compose.override.yaml trocker init

# Windows separator (Git Bash)
COMPOSE_FILE='compose.yaml;compose.override.yaml' trocker init
```

Use `COMPOSE_FILE` only to help init discover the top-level files. The generated `trocker.yaml` should pin the correct relative paths afterward, so normal Trocker commands do not depend on that temporary environment.

If the repository contains several independent stacks (`apps/api/compose.yaml`, `apps/web/compose.yaml`, etc.), do not combine them. Ask which stack Trocker should manage, or treat each as a separate workspace checkout if that is the project's intent.

## 2. Follow the full hierarchy

For each top-level file, read the YAML and follow these edges recursively. For example:

```text
compose.yaml                         # listed in trocker.yaml
├── include: infra/compose.yaml      # not listed
│   └── include: infra/db/compose.yaml
└── services.web.extends.file: base/web.yaml

compose.override.yaml                # listed after compose.yaml, if selected
```

The resulting manifest starts with:

```yaml
compose:
  - compose.yaml
  - compose.override.yaml
```

The services from every included/extended file are part of the loaded project and are candidates for endpoints and bindings. The files themselves are not manifest entries.

### `include`

Compose `include` is a list. Each entry is either a path string or an object:

```yaml
include:
  - common.yaml
  - path:
      - base/stack.yaml
      - base/stack.override.yaml
    project_directory: base/
    env_file:
      - base/.env
```

- A `path` may be one file or a list. A list is merged in order, first file first.
- Relative paths resolve against the active Compose project directory. Trocker sets that to the directory containing `trocker.yaml` for the top-level load, so an include in `deploy/compose.yaml` is resolved from the repository root, not from `deploy/`. Once a file is included, that file's own includes default to its directory. `project_directory` changes the base used for the included project's relative paths. `env_file` supplies interpolation values for that included project.
- This differs from running `docker compose -f deploy/compose.yaml` directly in some cases. Trust `trocker init`/`trocker plan` and the SDK's loading behavior, not a path guess based on the top-level file's location.
- Follow every included file's own `include` list. Detect cycles and let Compose report them rather than guessing.
- Files reached through `include` belong to the Compose project but do not belong in `trocker.yaml`'s top-level `compose:` list.
- Remote includes (for example Git or OCI resources) are not part of the supported offline setup path. Flag them instead of assuming they will resolve.

### `extends`

A service may extend another service:

```yaml
services:
  web:
    extends:
      file: base/compose.yaml
      service: web
```

or, within the same file:

```yaml
services:
  web:
    extends: common-web
```

Follow `extends.file` recursively. It is another Compose file edge, not a top-level manifest entry. Its path follows the same active-project-directory rules as `include`: a top-level file's `extends.file` is resolved from the manifest directory, while an included file's references default to that included file's directory. Extending a service from an included file is still resolved by the SDK. Let the SDK report missing services, unsupported merge cases, or cycles.

### Top-level overrides

Multiple top-level files are merged in the order listed. A Compose override may add services, override environment values, extend lists, and so on. Review the merged result conceptually, but let the SDK decide the final values. Do not copy an override's contents into the base file or into `trocker.yaml`.

## 3. Determine active services and profiles

A service with a `profiles` key is inactive unless one of its profiles is enabled. Trocker has no profile flag in `trocker.yaml`; profiles come from the environment, including Compose `.env` values such as `COMPOSE_PROFILES`.

Consequences:

- An endpoint must refer to an active service. If the service is profile-gated, confirm that the environment used to run Trocker enables it.
- Do not add a binding for a service that is inactive by default unless the user confirms the profile setup.
- `trocker plan` and `trocker doctor` validate against the active project, so they will expose an endpoint/service mismatch.

Do not print `.env` values. If needed, list only variable names, or ask the user which profiles are intended.

## 4. Inventory services and ports

For every active service, record:

- exact service name;
- image or build context;
- `ports` entries and their container targets;
- `expose` entries;
- protocols, including `/udp`;
- networks and shared-network relationships;
- named/anonymous volumes and bind mounts, noting whether each bind mount is writable: short syntax without `:ro`, or long syntax with `read_only` false or absent;
- whether a bind mount is a Docker socket or a directory containing one, for `docker_socket_services`;
- `depends_on`, health checks, and profile membership;
- environment keys (never values) that look like service addresses;
- unsupported fields listed in [the manifest reference](manifest-reference.md#unsupported-compose-features).

Port rules for endpoints:

| Compose declaration | Endpoint port |
| --- | --- |
| `ports: ["8080:8000"]` | `8000` (container target) |
| `ports: ["8080:8000/udp"]` | Unsupported for an endpoint |
| `ports: ["8000"]` | `8000` |
| `expose: ["8000"]` | `8000` |
| `expose: ["8000-8002"]` | The endpoint chooses one covered port, for example `8000` |
| Long syntax with `target: 8000` | `8000` |

Only TCP is supported. A service can declare a port in `ports` or `expose`; the endpoint's port must match one of those declarations. Declaring a port does not prove that the application listens on it.

Selecting candidates: a service with a Compose `ports:` publication is usually intended for host access and deserves an endpoint unless that publication is obsolete. A service with only `expose:` still needs an endpoint when another service or a native process consumes it through a binding. A purely internal port with no consumer does not need an endpoint.

Useful read-only checks:

```sh
# Optional: validate Compose syntax without printing resolved secrets.
docker compose -f compose.yaml -f compose.override.yaml config --quiet

# Optional: list services and profile names.
docker compose -f compose.yaml config --services
docker compose -f compose.yaml config --profiles
```

The Docker Compose CLI may not be installed even when Trocker works; the SDK is the authority. Never run a bare `docker compose config` and paste its output, because it can contain environment values and secrets.

## 5. Find real consumers before adding bindings

Search for the application's own configuration vocabulary, not just Compose:

- environment keys in Compose, `.env` (names only), Dockerfiles, and runtime configuration;
- source code reads such as `process.env.API_URL`, `os.Getenv`, `env(...)`, settings classes, or config schemas;
- URLs already used in development config, health checks, README instructions, frontend code, or native launch scripts;
- package scripts, Makefiles, `just` recipes, and workspace tooling that launch a local process.

Then classify the consumer:

| Consumer | Address form | Binding location |
| --- | --- | --- |
| Container calling another container | `endpoints.<alias>.container.*` | `bindings.<service>.environment` |
| Native process on the host | `endpoints.<alias>.host.*` | `bindings.<service>.native.environment` |
| Browser-facing URL emitted by a container | Usually `endpoints.<alias>.host.url` | `bindings.<service>.environment` |
| File-based application config | Explicit generated file | `bindings.<service>.files` or `.native.files` |

A container `host` URL is for a browser or another host-side consumer. From inside a container, `127.0.0.1` refers to that container, not the Docker host. Do not replace a working service-to-service URL with a host URL unless the application truly runs on the host or in a browser.

Native bindings use observed host publications from an already-running owned stack. `trocker run` does not start the dependency services for you; the project workflow must start them first, usually with `trocker up --detach SERVICE`.

For a container binding that uses a `container` address, the consumer and endpoint service must share a Compose network. For `up SERVICE`, the endpoint service must also be selected or reachable through Compose's dependency rules. If either condition is missing, report the blocker; do not rewrite the Compose network design.

Avoid speculative bindings. An endpoint without bindings is valid. It is better to leave an ambiguous binding out and explain why than to inject a value the application never reads.

## 6. Decide the manifest shape

After the inventory, the manifest should look like this:

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

The `compose:` list stays at the top-level files. The endpoint and binding decisions come from the consumer analysis, not from the directory layout of included files.

Add `read_write_paths` only for writable bind mount sources the user approved; read-only mounts need no entry. The path list, containment rules, and outside-the-checkout blocker are in [the manifest reference](manifest-reference.md#read_write_paths).

Add `docker_socket_services` only for services the user approved for Docker API access. Socket paths are not `read_write_paths` entries. The contract is in [the manifest reference](manifest-reference.md#docker_socket_services).
