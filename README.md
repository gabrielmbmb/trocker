# Trocker

Isolated development environments for Git worktrees, powered by Docker Compose.

**Worktrees isolate code, not running environments.** Coding agents can work on multiple branches at once, but each needs its own services, ports, and application URLs to test changes independently.

**Trocker gives each worktree its own stack.** Reuse your Compose definitions and let Trocker isolate resources, assign ports, and connect your apps—without wiring up every environment by hand.

## The workflow

**Configure once, start an environment in each worktree.** Use Trocker's skills with Codex, Claude Code, Pi, OpenCode, or another coding agent:

1. **Set up the project** with `trocker-setup`. The agent analyzes your Compose files and application settings, configures `trocker.yaml`, and validates it. Commit the manifest alongside your Compose files.
2. **Work in a Git worktree.** Create or open the worktree where you or your agent will make changes.
3. **Start its environment** with `trocker-up`. The agent registers the worktree if needed, starts its isolated stack, and reports the current addresses.
4. **Develop and test independently.** Other worktrees keep their own stacks and managed data.

## Install

```bash
brew tap gabrielmbmb/homebrew-tap
brew install --cask trocker
```

Or download a binary for macOS, Linux, or Windows (amd64/arm64) from [GitHub Releases](https://github.com/gabrielmbmb/trocker/releases).

You'll need Git, a Compose project, and a reachable local Docker daemon running Linux containers. Native application execution and generated configuration files require Linux or macOS.

## Set up your agent

Copy these directories into your agent's skills directory, keeping their contents intact:

| Skill | When to use it |
| --- | --- |
| [`trocker-setup`](skills/trocker-setup/) | Configure a project for Trocker; includes Compose-analysis and manifest references |
| [`trocker-up`](skills/trocker-up/) | Start an environment in a worktree that already has `trocker.yaml` |

If your agent doesn't discover skills automatically, give it a local copy and ask it to read the skill's `SKILL.md` and linked references.

### Set up the project once

From your project's checkout, ask:

> Set up Trocker for this project.

Review the configuration and confirm `trocker init` in a terminal when prompted. Initialization registers the workspace without creating Docker resources. If the agent cannot handle interactive confirmation, run `trocker init` yourself and let it continue.

The setup skill checks configuration without starting containers. Once validation passes, commit `trocker.yaml` so other worktrees can use it.

### Start each worktree's environment

Open your agent in the intended worktree and ask:

> Start this worktree's environment with Trocker.

The agent uses the existing configuration, checks the plan, and runs `trocker up --detach`. A new worktree needs its own confirmed registration. The agent then checks service state and reports observed endpoint addresses—no manual port assignment required.

**Use the reported addresses.** Ports can change after a restart. A published endpoint is not, by itself, proof that the application is ready.

## Everyday commands

You can manage the stack directly at any time:

| Command | Purpose |
| --- | --- |
| `trocker up --detach` | Create or update the stack and wait for running/healthy services |
| `trocker up --detach --build` | Rebuild images and start the stack |
| `trocker status` | Show workspace containers and their state/health |
| `trocker endpoints` | Show current addresses; add `--json` for machine-readable output |
| `trocker plan` / `trocker doctor` | Preview changes or check configuration without writes |
| `trocker stop [SERVICE...]` | Stop containers without deleting resources |
| `trocker start [SERVICE...]` | Restart existing containers without applying configuration edits |
| `trocker down` | Remove workspace containers and networks; retain volumes |
| `trocker down --volumes` | Also remove managed named and attached anonymous volumes |
| `trocker help COMMAND` | Show command options |

Use `up`, not `start`, to apply configuration changes. Service arguments to `up` select services and their Compose dependencies. Run `trocker up` without `--detach` to follow logs; end that foreground session before another mutating command for the same workspace.

**Data is retained by default.** Teardown verifies ownership and preserves workspace registration. Images and bind-mounted files are retained. Generated configuration files are never automatically cleaned up, even with `down --volumes`; old generations can retain secrets.

## Supported scope

Trocker supports **local Docker targets, ordinary Compose bridge networking, and IPv4 loopback TCP publications**. It replaces host port mappings, removes `container_name`, and isolates managed resource names and build tags.

Not every Compose project is supported unchanged. Trocker rejects unsafe sharing and unsupported features, including:

- Remote targets, UDP/IPv6 publications, and nonstandard network modes.
- External or explicitly named networks/volumes, writable bind mounts, and custom network addressing/drivers.
- Explicit anonymous mounts, deploy/replication settings, hooks/providers, privileged/device/namespace access, and advanced build access/tags.
- User-declared Compose secrets, configs, and models. Trocker's generated files are supported separately.

Built service names must be lowercase. The setup skill reports blockers rather than silently rewriting your Compose project. `trocker doctor` checks the supported subset; it is not a general security audit.

Trocker checks Compose and workspace ownership before changing resources. It does not automatically approve recreation prompts, remove orphans, or delete volumes after failed startup. Missing or conflicting registration requires explicit recovery; automatic recovery and forgetting are not implemented. `status` currently lists containers, not retained networks or volumes.

## Alternatives and advanced configuration

### Configure manually

**Skills are recommended, not required.** Run `trocker init` to propose a minimal `trocker.yaml`, or validate an existing one without rewriting it. Add endpoints and bindings explicitly, then run `trocker doctor` and `trocker plan` before starting the stack.

Keep service definitions in Compose:

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

This example requires Compose services named `backend` and `web`, with `backend` declaring TCP port `8000` through `ports` or `expose`. When starting only `web`, declare `backend` in its `depends_on` or select both services.

- **Container addresses** use Compose DNS, such as `http://backend:8000`.
- **Host addresses** use assigned loopback ports for local applications or browsers, not container-to-container requests.
- **Container bindings** override Compose environment values; **native bindings** apply only to `trocker run`.
- Every managed container and native application receives `TROCKER_WORKSPACE_ID`.

Applications must consume these settings at runtime; frontend build-time substitution is not provided. Endpoint declarations do not configure TLS. See [configuration and bindings](docs/architecture.md#configuration-and-bindings) for the full contract.

### Run your application outside Docker

Keep dependencies in Docker and run your application locally using the native bindings above:

```bash
trocker up --detach backend
trocker run web -- pnpm dev
```

`run` starts a native process, **not** a one-off container. The selected service must exist in Compose, but its container is not started. Referenced dependencies must already be running.

The child uses your current directory and inherited environment plus native bindings—not Compose environment values. Standard streams and exit codes pass through, and arguments are passed without shell expansion. Native execution supports Linux/macOS, runs with your normal permissions, and does not allocate the application's own listening port.

### Generate configuration files

For applications that read files rather than environment variables, declare templates under `bindings.<service>.files` for containers or `bindings.<service>.native.files` for local processes. Trocker renders private immutable files and exposes their paths through bindings; container delivery is read-only.

See the [skill's manifest reference](skills/trocker-setup/references/manifest-reference.md#generated-files) for an example and [generated configuration files](docs/architecture.md#generated-configuration-files) for validation, permissions, and retention rules. All users inside a receiving container can read its mounted file, and old generations are retained.

## Architecture

See [Architecture](docs/architecture.md) for component boundaries, identity, allocation, and safety contracts.
