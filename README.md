# Trocker

Isolated development environments for Git worktrees, powered by Docker Compose.

**Worktrees isolate code, not running environments.** Coding agents can work on multiple branches at once, but each needs its own services, ports, and application URLs to test changes independently.

**Trocker gives each worktree its own stack.** Reuse your Compose definitions, declare application bindings, and let Trocker isolate resources, assign ports, and connect your apps—without wiring up every environment by hand.

## Install

```bash
brew tap gabrielmbmb/homebrew-tap
brew install --cask trocker
```

Or download a binary for macOS, Linux, or Windows (amd64/arm64) from [GitHub Releases](https://github.com/gabrielmbmb/trocker/releases).

You'll need Git, a Compose project, and a reachable local Docker daemon running Linux containers. Native application execution and generated configuration files require Linux or macOS.

## Quick start

### 1. Let your coding agent configure the project

**Recommended:** use the [trocker-setup skill](skills/trocker-setup/SKILL.md) with Codex, Claude Code, Pi, OpenCode, or another coding agent. Tracing Compose files and wiring application bindings by hand is tedious; the skill guides your agent through that work.

Copy the complete [`skills/trocker-setup/`](skills/trocker-setup/) directory, including `references/`, into your agent's skills directory. Alternatively, give your agent a local copy and ask it to read `SKILL.md` and its references. From your project's checkout, ask:

> Use the trocker-setup skill to configure this project. Analyze the Compose hierarchy and application settings, configure trocker.yaml, and validate with trocker doctor and trocker plan. Don't start containers or change unrelated files.

Review the proposed configuration and confirm `trocker init` in a terminal when prompted. Initialization registers the workspace without creating Docker resources. If the agent cannot handle interactive confirmation, run `trocker init` yourself and let it continue.

### 2. Start your environment

Once setup checks pass:

```bash
trocker up --detach    # Start the stack and wait for running/healthy services
trocker endpoints     # Show addresses for configured endpoints
```

Commit `trocker.yaml` alongside your Compose files. Run `trocker init` in each additional worktree to register its separate workspace identity.

Use `trocker up` without `--detach` to follow application logs, or add `--build` to rebuild images. To start selected services and their Compose dependencies, use `trocker up --detach SERVICE...`.

## Manual configuration

**Prefer to configure it yourself?** Run `trocker init` to generate a minimal `trocker.yaml` using Compose's default file selection, or validate an existing one without rewriting it. Add endpoints and bindings explicitly, then run `trocker doctor` and `trocker plan` before starting the stack.

Keep service definitions in Compose and put Trocker endpoints and bindings in `trocker.yaml`:

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

This example assumes your Compose project defines `backend` and `web`, and that `backend` declares TCP port `8000` through `ports` or `expose`. When starting only `web`, declare `backend` in its `depends_on` or select both services.

- **Container addresses** use Compose DNS, such as `http://backend:8000`.
- **Host addresses** use an assigned loopback port, such as `http://127.0.0.1:24001`. Use these for local applications or browsers, not container-to-container requests.
- **Container bindings** override Compose environment values. **Native bindings** apply only to `trocker run`.
- Every managed container and native application receives `TROCKER_WORKSPACE_ID`.

Applications must read bindings at runtime; frontend build-time substitution is not provided. Ports are not guaranteed to stay the same—use `trocker endpoints` for current addresses. Declaring an endpoint does not configure TLS or guarantee application readiness.

See [configuration and bindings](docs/architecture.md#configuration-and-bindings) for supported fields and validation rules.

## Run your application locally

Keep dependencies in Docker while running your application directly on your machine:

```bash
trocker up --detach backend
trocker run web -- pnpm dev
```

`run` is a native-process command, **not** Compose's one-off container command. The selected service must exist in Compose, but its container is not started. Dependencies referenced by native bindings must already be running.

The application runs in your current directory with your inherited environment plus native bindings. It does not inherit the service's Compose environment. Standard input/output and exit codes pass through; arguments after `--` are passed directly without shell expansion.

Native execution is supported on Linux and macOS. It runs with your normal permissions, is not a supervisor, and does not allocate the application's own listening port.

## Generate application configuration

For applications that read files instead of environment variables, declare a template and explicit values:

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
      environment:
        APP_CONFIG: "{{ files.app_config.path }}"
```

Create `.trocker/app.json.tmpl`:

```gotemplate
{
  "apiUrl": {{ json .api_url }}
}
```

Trocker renders the file into private state before launching your application and supplies its path through `APP_CONFIG`. Templates must be inside the manifest directory tree; `output` is a filename, not a checkout path. Formats are `json` and `text`.

For container delivery, place `files` and `environment` directly under `bindings.web` instead of `native`, add an absolute `target` such as `/run/app.json`, and use container endpoint addresses where appropriate. `up` delivers files read-only through Compose configs; `start` keeps the existing generation. All users inside the receiving container can read the mounted file.

**Generated files are not automatically cleaned up.** Old generations, including any secrets they contain, remain in private state even after `down --volumes`.

See [generated configuration files](docs/architecture.md#generated-configuration-files) for template rules, delivery, and retention details.

## Manage your stack

| Command | Purpose |
| --- | --- |
| `trocker plan` | Preview configuration and isolation changes without writes |
| `trocker doctor` | Check unsupported configuration, bindings, and unsafe sharing |
| `trocker status` | Show workspace containers and their state/health |
| `trocker endpoints --json` | Get current endpoint addresses as JSON |
| `trocker stop [SERVICE...]` | Stop containers without deleting resources |
| `trocker start [SERVICE...]` | Restart existing containers and their dependencies |
| `trocker down` | Remove workspace containers and networks; retain volumes |
| `trocker down --volumes` | Also remove managed named and attached anonymous volumes |
| `trocker help COMMAND` | Show command options |
| `trocker completion bash` | Generate completion for Bash, Zsh, Fish, or PowerShell |
| `trocker version` | Show the installed version |

Use `up`, not `start`, to apply configuration changes. Docker-assigned ports may change after a restart.

Teardown verifies ownership and preserves workspace registration. Images and bind-mounted files are retained. `start`, `stop`, and `down` can operate even if your current configuration has changed or disappeared.

Foreground `up` holds the workspace mutation lock while attached; end that session before running another mutating command for the same workspace.

## Supported scope and safeguards

Trocker currently supports local Docker targets, ordinary Compose bridge networking, and IPv4 loopback TCP publications. It replaces host port mappings, removes explicit `container_name` values, and isolates managed resource names.

Not every Compose feature is supported. Trocker rejects unsafe sharing and unsupported features, including:

- Remote Docker targets, UDP publications, IPv6 publications, and nonstandard network modes.
- External or explicitly named networks/volumes, writable bind mounts, and custom network addressing/drivers.
- Explicit anonymous mounts, deploy/replication settings, hooks/providers, privileged/device/namespace access, and advanced build access/tags.
- User-declared Compose secrets, configs, and models. Trocker's generated files are supported separately.

Built service names must be lowercase. Run `trocker doctor` to check your project; it is not a general security audit.

Trocker checks both Compose and workspace ownership before changing resources. It does not automatically approve recreation prompts, remove orphans, or delete volumes after failed startup. Correct the reported problem and retry.

Workspace identity is stored outside your project files. Missing or conflicting registration requires explicit recovery; automatic recovery and forgetting are not yet implemented. `status` currently lists containers, not retained networks or volumes.

## Architecture

See [Architecture](docs/architecture.md) for component boundaries, configuration contracts, identity, allocation, and safety.
