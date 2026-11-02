# Trocker

A Go CLI for isolated Docker Compose stacks per Git worktree.

**Status:** `init`, `plan`, `doctor`, `up`, `start`, `stop`, `down`, `status`, `endpoints` (including `--json`), help, version, and shell completion work. `init` validates Compose configuration, resolves the selected local Docker daemon, and registers a persistent workspace identity after interactive confirmation. It does not create Docker resources. Native `run`, including optional generated configuration files, works on Linux and macOS. Invalid arguments return exit status 2.

## Commands

```bash
trocker init                       # Confirm configuration and workspace registration
trocker plan                       # Preview isolation/configuration changes
trocker up [SERVICE...]            # Create and start; supports --detach/-d and --build
trocker start [SERVICE...]         # Start existing stopped containers
trocker stop [SERVICE...]          # Stop without removing containers
trocker down                       # Remove containers/networks; retain volumes
trocker down --volumes             # Also remove managed volumes; shorthand: -v
trocker run web -- pnpm dev        # Run a native process with service bindings
trocker status                     # Show workspace resources and state
trocker endpoints --json           # Show resolved addresses
trocker doctor                     # Check unresolved references and unsafe sharing
trocker version
trocker completion bash            # Also supports zsh, fish, and powershell
trocker help down                  # Or: trocker down --help
```

All commands above are implemented within the initial supported subset. `run` is deliberately a native-process command, not Compose's one-off container command. Use `--` to separate its service from the child command and arguments.

## Initialization

Run `trocker init` from a Git checkout with a reachable local Linux-container Docker target. It validates an existing `trocker.yaml` without rewriting it, or proposes a minimal manifest using the Compose SDK's default file selection. Endpoint schemes and application bindings are not guessed; add them explicitly. Confirmation requires a terminal; noninteractive input is rejected.

Registration stores a UUID and target associations in the user-level registry, plus a matching `trocker-workspace-id` marker in the per-worktree Git administrative directory. Repeated initialization and supported linked-worktree moves preserve the UUID. Missing/conflicting associations require explicit recovery; automatic recovery and forgetting are not implemented.

## Planning and startup

After `init`, run `trocker plan` or `trocker doctor` to check isolation, bindings, and existing resource ownership without writes. Plans never print resolved environment values or promise a particular host port.

`trocker up --detach` creates/starts the stack and waits up to two minutes for Compose running/healthy readiness. `trocker up` attaches application logs until the foreground session ends. Progress and foreground log formatting use Compose's native renderers on stderr; Trocker's endpoint summary follows on stdout. Non-terminal output, `NO_COLOR`, `TERM=dumb`, and CI use plain progress. SDK failure details remain sanitized and build-step logs remain suppressed; resource progress is displayed. `--build` rebuilds workspace-tagged images; service arguments select services and their Compose dependencies. Bindings to another service require it to be included or declared in `depends_on`.

The initial policy replaces publications with loopback TCP ports, removes `container_name`, isolates managed network/volume names and build tags, and injects `TROCKER_WORKSPACE_ID`. Docker assigns ports unless a container binding needs the address before startup; Trocker coordinates those ports. Endpoints declared with only `expose` also receive a publication.

Unsafe sharing is rejected: external/named networks or volumes, writable bind mounts, custom network addressing/drivers, UDP publications, and nonstandard network modes. Explicit anonymous mounts, replication/deploy settings, hooks/providers, privileged/device/namespace settings, secrets/configs/models, and advanced build access/tags are outside this first slice. Built service names must be lowercase. `doctor` checks this supported subset; it is not a general security audit.

Existing resources must match both Compose and workspace ownership. `up` never automatically approves Compose recreation prompts, removes orphans, or rolls back volumes on failure. Retry after correcting the error; registration is preserved. Networks/volumes are ownership-checked even though `status` does not yet list them.

## Host bindings

A container can advertise an endpoint's browser-facing URL using the existing schema:

```yaml
bindings:
  web:
    environment:
      INTERNAL_API_URL: "{{ endpoints.api.container.url }}"
      BROWSER_API_URL: "{{ endpoints.api.host.url }}"
```

With an `api` endpoint on container port `8000`, these might resolve to `http://api:8000` and `http://127.0.0.1:24001`. The host URL is for a browser on your machine, not a request from inside the web container. Applications must read these variables at runtime; build-time frontend substitution is not provided.

Trocker coordinates only the required host ports before Compose starts, reuses verified mappings or available remembered ports, and refreshes bindings on genuine bind-conflict retries (three startup attempts maximum). Preferences are not reservations or guarantees of stable ports. Successful detached startup verifies actual publications before remembering them. `plan` previews requirements without allocating ports or exposing environment values.

The supported allocation scope remains local-host IPv4 loopback TCP. No manifest changes or hardcoded host ports are required; UDP, IPv6 publications, and remote targets remain unsupported.

## Running an application locally

Keep dependencies in Docker and select explicit native bindings for your local application:

```yaml
bindings:
  web:
    native:
      environment:
        API_URL: "{{ endpoints.api.host.url }}"
```

```sh
trocker up --detach api
trocker run web -- pnpm dev
```

The selected service must exist in Compose, but its container is not started. The command runs in your current directory with your inherited environment, then native binding overrides and the authoritative workspace UUID. Compose/container environment values are not implicitly inherited. Executables are located using the invoking process's PATH; arguments are passed directly, without shell expansion.

Dependencies referenced by native bindings must already have owned, running host publications. These addresses are a startup snapshot, not a promise that dependencies remain running. Trocker's preparation creates no containers, port claims, or registry entries. Only explicitly declared native files are generated; no locks are held during the child lifetime. The child runs with your normal permissions, not in a sandbox. Commands with only literal/workspace-ID bindings need no daemon connection.

Child stdin/stdout/stderr and terminal detection are preserved, and child exit codes—including 2—propagate without an extra Trocker error. Linux/macOS forward SIGINT/SIGTERM to the owned process group, allow five seconds after cancellation before force-killing it, and clean up remaining group members when the leader exits. Terminal foreground ownership/settings are restored. Descendants that deliberately detach into another session/group or elevate privileges are outside this cleanup contract. Windows native execution is not yet supported.

`run` is not a supervisor and does not allocate the native application's own listening ports.

## Generated native configuration

Applications that read a configuration file can opt in explicitly:

```yaml
bindings:
  web:
    native:
      files:
        app_config:
          template: .trocker/app.json.tmpl
          output: app.json
          format: json
          values:
            api_url: "{{ endpoints.api.host.url }}"
            workspace_id: "{{ workspace.id }}"
      environment:
        APP_CONFIG: "{{ files.app_config.path }}"
```

The template uses Go `text/template` with only the supplied string values and a `json` encoder:

```gotemplate
{
  "apiUrl": {{ json .api_url }},
  "workspaceId": {{ json .workspace_id }}
}
```

Templates must be regular files inside the manifest directory tree; outputs are filenames, never checkout paths. `format` is `json` or `text`. Inputs and outputs are limited to 1 MiB each. Missing ordinary template keys, invalid JSON, or unavailable dependencies prevent launch. `init`, `plan`, and `doctor` check inputs/syntax without rendering; JSON content is validated by `run`.

Files live in private state (`0700` directories, `0600` files). Unchanged output reuses the current generation; changed output gets a new immutable snapshot, preserving running consumers' files. Publication uses only a short consumer lock. **There is no automatic cleanup:** old generations, including old secrets, remain private but retained, even after `down --volumes`. Container delivery is described below. See the [generated-file contract](docs/generated-files-proposal.md).

The repository's demo worker has this configured. With the demo workspace initialized:

```sh
./bin/trocker up --detach api
./bin/trocker run worker -- go run ./examples/native-config
```

The native example opens `APP_CONFIG`, calls the API, verifies its workspace identity, and prints the JSON response. Trocker does not rewrite arguments; for applications accepting only flags, use an explicit wrapper such as `sh -c 'exec myapp --config "$APP_CONFIG"'`.

## Generated container configuration

On Linux/macOS hosts, declare container files under `bindings.<service>.files`, with a required absolute container `target`:

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

`up` renders selected consumers and delegates read-only delivery to Compose's file-backed configs. Host `port`/`url` inputs participate in allocation and are rerendered on bind-conflict retries. Changed bytes select a new opaque generation, causing Compose to recreate the consumer; unchanged bytes do not. `start` keeps the existing mount and generation. Native and container files have separate scopes.

Container files are `0444` inside **private `0700` host directories** and are individually mounted read-only. Other ordinary host users cannot traverse the private tree; all UIDs inside the selected container can read its mounted file. This supports non-root applications without chowning host files or exposing the generated directory. It is not per-user secret isolation inside a container. The daemon must be able to access the private state directory. Windows-host delivery is rejected pending a tested ACL policy; remote targets remain unsupported.

Overlapping mounts/files and special targets such as `/proc`, `/sys`, `/dev`, and Docker-managed hostname/DNS files are rejected. Arbitrary user-declared Compose configs/secrets remain outside the supported subset. Old generations remain retained, including after `down --volumes`.

The repository's container worker now reads its API address from `examples/container-config/api-url.txt.tmpl` via `/run/trocker-api-url`. Run `./bin/trocker up --detach worker` to exercise it in an initialized demo workspace.

## Stopping and teardown

- `trocker stop [SERVICE...]` stops containers without deleting resources.
- `trocker start [SERVICE...]` starts the existing containers and their Compose dependencies. It preserves IDs and configured publications, not necessarily Docker-assigned host port numbers. Fixed-port conflicts fail; run `trocker endpoints` for current addresses and use `up` to apply configuration changes.
- `trocker down` removes owned workspace containers and networks while retaining volumes.
- `trocker down --volumes` also removes verified managed named volumes and attached anonymous volumes using Engine's removal semantics. Unattached anonymous volumes are not swept. Images, explicitly named external volumes, and bind-mounted files are retained.

These commands use Compose's native progress renderer and live-resource reconstruction. They work even if the current Compose/manifest files have changed or disappeared. Foreign ownership, unsupported container metadata, and ambiguous retained-resource associations block destructive operations. Every teardown preserves workspace registration.

Foreground `up` holds the workspace mutation lock until the session ends; interrupt that session before invoking another mutating command for the same workspace. Detached `up` releases the lock once startup completes.

## Observations

`status` reports Compose-discovered workspace containers, their state/health, and missing configured services. `endpoints` reports actual host publications for explicitly configured endpoints; `--json` provides the versioned schema in the contracts. Both commands require confirmed workspace/target registration and verify container ownership without changing files or Docker resources.

Published addresses do not guarantee application readiness. Stopped or missing publications have no host address. Replicated endpoint services and multiple matching publications (including dual-stack bindings) are rejected as ambiguous. Container queries do not yet inventory retained networks or volumes, or containers excluded by Compose's discovery rules.

## Development

Use Go **1.27.3**, Git, and [just](https://github.com/casey/just). Workspace tests invoke the installed Git executable against disposable repositories. With an existing Go installation that supports toolchain selection, the recipes download and select the version declared in `go.mod` automatically. They do not replace your global Go installation.

```bash
just tools       # Install pinned development tools under .tools/
just check       # Module consistency, lint/format checks, race tests, executable smoke checks
just vuln        # Known-vulnerability scan; requires network access
just integration # Requires local Docker and image-pull access; creates disposable labeled containers
just fmt         # Apply formatting
just build       # Build bin/trocker (bin/trocker.exe on Windows)
just run --help
just bench       # Informational command microbenchmarks
```

Recipes use Bash, curl, and a SHA-256 utility; on Windows, use Git Bash. Race tests require a working C toolchain. Tool downloads are checked and cached in version-specific directories. Build outputs and tools are ignored by Git.

CI runs tests and executable smoke checks on Linux, macOS, and Windows, plus Docker integration tests, static analysis, and vulnerability scanning on Linux. The same recipes run locally and in CI.

See [Go static analysis baseline](docs/reports/go-static-analysis-2026.md) for the researched tool selection, pinned versions, security checks, and update procedure.

## Architecture

[Architecture](docs/architecture.md) is the main reference for components, SDK delegation, command workflows, identity, allocation, persistence, and safety. It distinguishes the intended design from the current CLI scaffold.

See the [implementation plan and validation](docs/implementation-plan.md) for the ordered implementation slices, required behavior, tests, and completion gates.

The [initial implementation contracts](docs/initial-contracts.md) define the first manifest schema, binding rules, identity/target behavior, and acceptance cases. Manifest validation, Git identification, local target resolution, persistent registration, and minimal interactive initialization are implemented. Container observations are implemented. Pure planning and initial `up` are implemented. Existing-resource lifecycle commands are implemented. Host container bindings and coordinated local loopback TCP allocation are implemented. Foreground native execution and optional generated native configuration files are implemented on Linux/macOS. Generated container files are also supported on Linux/macOS hosts through read-only Compose configs. Retained resource listing and generated-file garbage collection remain unimplemented.

### Design reports

- [Worktree isolation and endpoint bindings](docs/reports/worktree-isolation-and-endpoint-bindings.md): explicit-first configuration, collision-aware port allocation, and workspace identity.
- [Go architecture and local state](docs/reports/go-architecture-and-state.md): official Compose SDK integration, minimal persistent state, optional generated files, lifecycle recovery, and performance requirements.
