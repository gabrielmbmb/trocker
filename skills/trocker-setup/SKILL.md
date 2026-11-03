---
name: trocker-setup
description: Prepare a Git checkout to use Trocker. Use when installing the Trocker CLI, creating or validating trocker.yaml, tracing Docker Compose files through includes, overrides, and extends, declaring endpoints, bindings, writable host paths, shared host paths, additional build contexts, Docker socket access, and host namespace/capability/device access, or checking setup readiness with trocker init, plan, and doctor.
---

# Configure Trocker for this project

Turn the current Git worktree into a usable Trocker workspace:

1. Ask before installing the Trocker CLI when `trocker` is not on `PATH`.
2. Generate `trocker.yaml` with `trocker init` when it is absent.
3. Read the complete active Compose hierarchy, not just the top-level files.
4. Add only the endpoints, bindings, writable host paths, and host-level access the project actually needs.
5. Validate the result with Trocker before handing it back.

`trocker.yaml` is explicit project configuration. Trocker does not infer endpoint schemes or application bindings at runtime, so setup is where those decisions are made.

## Safety rules

- Work from the Git worktree root. `trocker.yaml` belongs there.
- Never edit Compose files, Dockerfiles, application code, `.env`, or unrelated configuration just to make Trocker accept the project. Report unsupported Compose features instead.
- Never add a `read_write_paths` entry without explicit confirmation. Each entry lets containers modify that path on the host, so show the service, host path, and container path before asking.
- Never add a service to `docker_socket_services` without explicit confirmation. It grants control of the Docker daemon, including containers and images outside this workspace, so show the service and socket path before asking.
- Never add a `host_access` grant without explicit confirmation. It exposes host process state, host capabilities, or devices and is not workspace-isolated. Show the exact Compose declaration and matching grant before asking.
- Never print `.env` values, Compose environment values, secrets, generated files, or full resolved Compose configuration. List keys or use targeted queries only.
- Do not run `trocker up`, `start`, `stop`, `down`, or `run` unless the user explicitly asks for a runtime test. `init`, `plan`, and `doctor` are the normal setup checks.
- Do not confirm registration on the user's behalf. With a terminal, run `trocker init` and let them answer the prompt. Without one, run `trocker init --print-proposal`, show them the proposal and its digest, and pass that exact digest to `trocker init --confirm` only after they approve it. Never pipe `y`, pick a digest yourself, hand-write registration state, or edit Trocker's registry.
- Do not delete or reset Trocker state to clear a registration, ownership, or move error. Preserve it and report the recovery requirement.
- Keep changes scoped. Do not rewrite an existing valid manifest with a fresh minimal one.
- Ask before installing Trocker or changing the user's toolchain. Never install it silently.

## 1. Preflight

From the repository root, confirm:

```sh
git rev-parse --show-toplevel
command -v trocker || echo 'trocker: not installed'
docker info --format '{{.ServerVersion}} {{.OSType}}'
```

- `trocker init` requires a Git checkout and a reachable local Docker target running Linux containers. Docker Desktop is local when published ports reach the host. If the Docker CLI is unavailable, rely on `trocker init`/`trocker plan` for target checks.
- Use `trocker` from `PATH`. Run `trocker version` once it is available; if it is already installed, report its version and continue without upgrading it.
- If `trocker` is missing, tell the user it is not installed and ask whether to install it. Before asking, determine the concrete install method and present it explicitly:
  - With Homebrew: show the exact commands, `brew tap gabrielmbmb/homebrew-tap` and `brew install --cask trocker`.
  - Without Homebrew: show the latest release URL from <https://github.com/gabrielmbmb/trocker/releases/latest>, the archive name for the current OS and architecture (`trocker_<version>_<os>_<arch>.tar.gz`, or `.zip` on Windows), the matching `trocker_<version>_checksums.txt` verification, and the writable directory already on `PATH` where the binary would be placed. Do not propose `sudo` or system directories.
- Install only after the user explicitly confirms. If the user declines, stop and ask for the binary location; do not continue with setup. Only fall back to `./bin/trocker` when working inside the Trocker source checkout.
- After a confirmed install, rerun `trocker version` and report what was installed. If installation fails or no writable `PATH` directory exists, stop and ask the user for the binary location.
- Inspect `trocker.yaml` if it exists. Also note whether `.env` or the environment sets `COMPOSE_FILE`, `COMPOSE_PROFILES`, or `COMPOSE_PROJECT_NAME`. Do not print their values.
- Check `git status --short` before editing so unrelated work is preserved.

## 2. Initialize when needed

If `trocker.yaml` is absent:

```sh
trocker init
```

Run it in a real terminal or PTY. The user must see the proposal and confirm it. The generated manifest contains only the SDK-selected top-level Compose files; empty `read_write_paths: []`, `docker_socket_services: []`, `host_access: {}`, `endpoints: {}`, and `bindings: {}` are expected at this stage.

Without a terminal, the same confirmation is available as a digest, and the agent still does not approve it:

```sh
trocker init --print-proposal   # prints the proposal and a digest; writes nothing
trocker init --confirm <digest> # registers only while that proposal still matches
```

The digest covers the effective manifest, the Docker target, whether the manifest would be created, and whether a moved checkout would be relinked. It excludes the checkout path and the workspace identity, so one approved digest registers any number of worktrees that share that configuration; editing the manifest or switching daemons requires a new approval.

- If init cannot find a Compose file, do not hand-write `trocker.yaml`. First locate the intended top-level file(s) using [Compose analysis](references/compose-analysis.md), then rerun init with an explicit selection, for example:
  ```sh
  COMPOSE_FILE=deploy/compose.yaml:deploy/compose.override.yaml trocker init
  ```
  Use `:` as the separator on Unix-like systems and `;` on Windows. Remove the temporary `COMPOSE_FILE` afterward; the manifest should pin the files itself.
- If the project has multiple unrelated stacks, stop and ask which stack Trocker should manage. Do not merge unrelated stacks into one manifest.
- If `trocker.yaml` already exists, read it and continue. Do not run init again until the configuration is final; init validates an existing manifest rather than rewriting it.

If `init` reports an invalid existing manifest, continue with the analysis and fixes below, then rerun init.

## 3. Analyze the full Compose hierarchy

Read [Compose analysis](references/compose-analysis.md) before deciding what belongs in `compose:`. In short:

- The manifest's `compose:` list contains the top-level Compose files, in merge order. It does not contain files pulled in by `include` or `extends`.
- Follow every top-level file recursively through:
  - `include` entries and their nested includes;
  - service `extends.file` references;
  - explicit top-level overrides selected by `COMPOSE_FILE` or SDK defaults.
- Resolve paths with Compose semantics relative to the active Compose project directory. Trocker uses the manifest directory as the project working directory, so a top-level file in `deploy/` resolves `include` and `extends.file` from the repository root, not from `deploy/`. An included file's own includes default to that included file's directory (or its declared `project_directory`). Account for the `project_directory` and `env_file` options when present.
- Determine which services are actually active. Profile-gated services must be active through the environment when Trocker is used; there is no profile flag in `trocker.yaml`.
- Build an inventory of services, declared container ports (`ports[].target` and `expose`), profiles, networks, volumes, bind mounts (writable or read-only, including Docker socket mounts), `build.additional_contexts`, `pid`, `ipc`, `cap_add`, `devices`, unsupported features, and every environment key whose value is a service address.
- Distinguish top-level files from included files. If init proposed the wrong ones, decline, correct the selection, and retry.

Do not flatten included files into `trocker.yaml`. The Compose SDK resolves and merges the hierarchy for each command.

## 4. Declare writable and shared host paths, build contexts, and host-level access

A bind mount is writable unless the mount declares `:ro` (short syntax) or `read_only: true` (long syntax). Trocker rejects any writable bind mount whose host source is not declared under `read_write_paths`, because that mount lets a container modify files in the checkout.

From the step 3 inventory, list every writable bind mount with its resolved host source and container target. Then:

- Declare a host source, never a container target. `read_write_paths` describes the checkout, not paths inside the container.
- A source is allowed when it equals a declared path or is nested under it. `.:/app` can only be declared as `.`; `./packages/api:/app` can be declared as `./packages/api`.
- Declare the narrowest path that covers the sources the user approves. One parent entry is fine when several approved mounts share it; do not declare `.` for a single nested mount.
- Read-only mounts need no entry. Do not declare them.
- A source outside the checkout cannot be declared in `read_write_paths`. Docker sockets use `docker_socket_services`; other external paths use `shared_write_paths`. Do not move, copy, or rewrite the Compose mount to work around a restriction.

Show a table of service, host source, and container target, and ask once which paths to declare. Add entries only after the user confirms; never add a path speculatively.

```yaml
read_write_paths:
  - .
```

Confirm the result in `trocker plan`, which lists each writable mount as `Writable mount: <host source> -> <container target>`. The full contract is in [the manifest reference](references/manifest-reference.md#read_write_paths).

### Shared host paths

A writable mount outside the checkout is allowed only when its host source is declared under `shared_write_paths`. These are machine-wide paths that the host and every other workspace also use; a credentials cache such as `~/.aws/sso/cache` is the common case.

```yaml
shared_write_paths:
  - ~/.aws/sso/cache
```

- Declare a host source, never a container target, and prefer the narrowest path that covers the approved mount.
- A leading `~/` is the only expansion. Do not use relative paths, `~user/...`, or environment variables.
- Paths inside the checkout belong in `read_write_paths`; paths that contain the checkout, the home directory itself, and the filesystem root are rejected.
- Declare a path only after confirming that the container must write there. Read-only external mounts need no entry.
- Mounts using a shared path are created with `create_host_path: false`, so the directory must already exist.
- Trocker never creates, labels, or deletes these paths; teardown does not clean them up.
- A declaration never authorizes the Docker API socket.
- `trocker plan` reports them as `Shared writable mount: <host path> -> <container path>` and states how many mounts are shared.

The full contract is in [the manifest reference](references/manifest-reference.md#shared_write_paths).

### Additional build contexts

Compose `build.additional_contexts` lets an image build read host directories beyond its build context. Trocker rejects any entry whose source is not declared under `build_contexts`.

```yaml
build_contexts:
  - packages
```

- Declare a host source, never the context alias. A source is allowed when it equals a declared path or is nested under it.
- Only paths inside the checkout can be declared. A context pointing at a home-directory cache or another external path is a blocker.
- `service:<name>` references and remote contexts are unsupported. Report them as blockers; do not rewrite the Compose file to work around them.
- Do not declare a path merely because a build exists. Ask once, as with `read_write_paths`.
- `trocker plan` reports each context as `Build context: <name> -> <host path>`.

The full contract is in [the manifest reference](references/manifest-reference.md#build_contexts).

### Docker socket mounts

A bind mount of a file named `docker.sock`, of the conventional socket paths (`/var/run/docker.sock`, `/run/docker.sock`), or of a directory containing those paths is Docker API access, not an ordinary writable path. `read_write_paths` cannot grant it, and a read-only mount is still API access. List each service the user approves under `docker_socket_services`:

```yaml
docker_socket_services:
  - localstack
```

- Confirm the service actually needs the Docker API: local AWS emulation, container log viewers, and metrics agents are common legitimate cases.
- A service that sets Compose `use_api_socket: true` also needs an entry.
- Do not add a service speculatively or merely because a socket mount is present. Ask once, as with `read_write_paths`.
- Do not add the socket path to `read_write_paths`.
- `trocker plan` reports the access as `Docker API: <host socket>` or `Docker API: use_api_socket`.
- Access is host-level and not limited to this workspace. It does not make the project safe to share with untrusted images.

The full contract is in [the manifest reference](references/manifest-reference.md#docker_socket_services).

### Host namespaces, capabilities, and devices

Trocker rejects `pid: host`, `ipc: host`, `cap_add`, and `devices` unless the service has a matching `host_access` grant. Metrics and logging agents commonly need `pid: host`; capabilities and devices should be granted only for a concrete requirement.

```yaml
host_access:
  datadog-agent:
    pid: true
    ipc: true
    capabilities: [SYS_PTRACE]
    devices:
      - source: /dev/kmsg
        permissions: rw
```

- `pid: true` authorizes `pid: host`; `ipc: true` authorizes `ipc: host`. `ipc: private` needs no grant.
- Other `pid` and `ipc` modes, such as `pid: service:...` or `ipc: shareable`, remain unsupported. Report them as blockers.
- `capabilities` authorizes Compose `cap_add` entries. Declare exact names, never `ALL`; a `CAP_` prefix is ignored.
- `devices` authorizes a device source and exact permissions (`r`, `w`, `m`). Every manifest device entry must state permissions explicitly; match the Compose declaration, and use `rwm` when Compose omits them.
- `privileged`, `device_cgroup_rules`, `uts: host`, and `cgroup: host` remain unsupported.
- Do not add a grant merely because Compose has the field. Ask for the exact service and grant, as with `read_write_paths`.
- `trocker plan` reports actual access under `Host access` and warns that it is not workspace-isolated.

The full contract is in [the manifest reference](references/manifest-reference.md#host_access).

## 5. Declare endpoints

Add an endpoint for each service address that the application, another service, a browser, or a native process needs to consume. Existing Compose `ports:` publications are usually evidence of intended host access; keep a corresponding endpoint unless the publication is clearly obsolete. A service consumed only through a container binding still needs an endpoint; Trocker creates its loopback publication. Skip ports that are purely internal and never addressed by anything else.

```yaml
endpoints:
  api:
    service: backend       # exact active Compose service name
    port: 8000             # container/listen port, not the published host port
    scheme: http           # http, https, ws, wss, or tcp
```

Check every endpoint:

- The alias matches `[a-z][a-z0-9_]*`.
- The service is active in the loaded project and exists by exact name.
- The port is declared as TCP by that service through `ports[].target` or `expose`. Ranges count only for the specific ports they cover.
- The scheme is verified from application configuration, health checks, documentation, or the service protocol. Do not guess HTTP vs HTTPS vs raw TCP. `https`/`wss` describe an existing TLS listener; Trocker does not add TLS.
- Use `tcp` for protocols without a URL. Trocker cannot render `url` references for a `tcp` endpoint.
- Do not add `public`; that address representation is unsupported.
- Use one clear alias per scheme. YAML duplicate keys are rejected.
- Declare a second alias for one service port when consumers need two schemes, such as `gateway` on `http` and `gateway_ws` on `ws`. Aliases of one port share a single publication.
- A container port that the service declares neither in `ports` nor in `expose` cannot be an endpoint, even when another service addresses it. Report the Compose declaration that would be needed; that address cannot be bound until it exists.
- An endpoint or binding must name an active service, so declaring one for a service behind an inactive profile fails validation for the whole manifest. Treat the profile as a prerequisite for every Trocker command, or leave that service unwired and report it.

When the scheme, listening port, or consumer is ambiguous, ask the user or omit the endpoint. A smaller valid manifest is better than an invented one.

## 6. Add bindings only when the application consumes them

Bindings are runtime configuration overrides. Add one only after finding the actual environment key or configuration value the application reads.

Work from an address inventory, not from the Compose `ports:` list. Collect every environment key or configuration value that holds a service address, from Compose `environment` entries, `.env` key names, and the application's own settings, and record a decision for each before writing the section:

| Key | Consumer | Facing | Decision |
| --- | --- | --- | --- |
| `BACKEND_PUBLIC_URL` | `backend` | host, public redirect base | `endpoints.api.host.url` |
| `GATEWAY_WS_URL` | `web` | host, browser socket | `endpoints.gateway_ws.host.url` |
| `SERVICE_API_URL` | `web` | container | `endpoints.api.container.url` |
| `GATEWAY_BASE_URL` | `backend` | container, port not declared | leave unbound, report |

Bind the host-facing and port-bearing values, because Trocker assigns those addresses. Leave a constant service-DNS value alone: Trocker isolates networks and container names, not service names, so `http://redis:6379` keeps resolving. Do not override a value that a secret manager or deployment may point at a real cloud endpoint. Every key in the inventory is bound, deliberately unbound with a reason, or reported as a blocker.

- **Container consumer:** `bindings.<service>.environment` with `{{ endpoints.<alias>.container.host|port|url }}`. Use this for server-side container-to-container traffic. The consumer and endpoint service must share a Compose network.
- **Native consumer:** `bindings.<service>.native.environment` with `{{ endpoints.<alias>.host.host|port|url }}`. Native bindings must not use `container` references.
- **Browser-facing value:** a container may need a `host` URL for a browser on the developer machine. That address is not automatically reachable from inside a container.
- **Generated files:** only for applications that explicitly read a file. Follow [the manifest reference](references/manifest-reference.md) and keep templates/outputs explicit. Do not add generated files speculatively.

```yaml
bindings:
  web:
    environment:
      INTERNAL_API_URL: "{{ endpoints.api.container.url }}"
    native:
      environment:
        API_URL: "{{ endpoints.api.host.url }}"
```

Rules:

- Preserve the exact environment key the application uses. Do not rename or invent keys.
- Bindings are keyed by service name. A service declared with `extends` does not inherit the extended service's binding; repeat the entries it needs.
- A container whose public base URL points at itself needs a `host` reference to its own service, such as its AuthKit redirect base. That reference makes `plan` coordinate the port before startup.
- Do not bind values consumed only at image build time.
- Do not add `TROCKER_WORKSPACE_ID`; Trocker injects it into every managed container and native child.
- `files.<name>.path` may be referenced only in the environment of the consumer declaring that file.
- Literal text plus references is allowed. There are no functions, conditionals, shell expansion, or recursive substitutions.
- Do not put credentials in bindings. Keep secrets in their existing mechanism.
- If a required consumer/network relationship is missing, report the blocker instead of modifying Compose. An endpoint without bindings is valid, but that must follow from the address inventory, never from an unexamined omission.

## 7. Write and validate

Update `trocker.yaml` with the same `version: 1` and the verified top-level Compose list. Prefer manifest-relative paths. Keep unrelated existing content stable.

Then validate in this order:

1. If the workspace has not been registered yet, run `trocker init` again. It validates the existing manifest and preserves any existing UUID. If you are unsure, `trocker plan` reporting that the workspace is not registered is the signal to run init. Use the digest form from step 2 when no terminal is available.
2. Run the read-only checks:
   ```sh
   trocker doctor
   trocker plan
   ```
   `doctor` and `plan` share the same isolation, binding, and ownership checks. `plan` also shows the workspace/project name, isolated resources, selected services, writable mounts, build contexts, Docker API access, host access, and ports that need coordination.

   `plan` renders host references as `<allocated-at-startup>` and prints no resolved container values, so it proves that the wiring parses, not that it targets the right address. Re-read every binding against the step 6 inventory; a binding that names the wrong key or alias still passes.
3. Fix only `trocker.yaml` when validation reports a manifest problem. If validation reports an unregistered workspace or a target that is not associated, run `trocker init` and confirm. If it reports an undeclared writable bind mount, a shared host path, or a build context, return to step 4 and confirm the path with the user; report it as a blocker when a shared path contains the checkout or when a build context is a `service:` reference or remote. If it reports Docker socket access or host access, return to the corresponding subsection in step 4 and confirm the service and grant with the user. If it reports an ambiguous move, a missing marker, a foreign resource, or an unsupported Compose feature, stop and report the exact blocker. Do not work around it by deleting state or rewriting Compose.
4. Re-run `trocker doctor` and `trocker plan` until both pass.

Do not claim the project is ready if either check could not run because Docker was unreachable, registration was missing, or confirmation was unavailable.

### Optional runtime check

Only when the user asks to prove runtime behavior:

```sh
trocker up --detach
trocker endpoints --json
```

This may build or pull images and create containers. Verify the expected endpoints are `published`; publication does not prove application readiness. Do not tear the stack down or delete volumes unless the user asks. Use `trocker down` (without `--volumes`) for normal cleanup.

## 8. Hand off

Report concisely:

- the Compose hierarchy and which files are top-level in the manifest;
- endpoints, bindings, writable host paths, Docker socket services, and host access grants added, with the reason for each;
- the address inventory from step 6, including every key deliberately left unbound and why;
- the result of `trocker init`, `trocker doctor`, and `trocker plan`;
- any assumptions or blockers;
- the next command the user should run, normally `trocker up --detach`.

## Completion checklist

- [ ] `trocker` is installed and `trocker version` succeeds.
- [ ] `trocker.yaml` exists and `version` is `1`.
- [ ] `compose:` lists the correct top-level files in order; includes/extends are not listed.
- [ ] Every included/overridden file and active service has been reviewed.
- [ ] Every endpoint uses an active service and a declared TCP container port.
- [ ] Every address-bearing environment key in the project is bound, deliberately left unbound with a reason, or reported as a blocker.
- [ ] Every binding matches a real runtime consumer and a supported address reference, and is repeated for each service that `extends` a bound consumer.
- [ ] Every writable bind mount is declared in `read_write_paths` after user confirmation, or is a Docker socket handled through `docker_socket_services`, or is reported as a blocker.
- [ ] Every service with Docker API access is declared in `docker_socket_services` after user confirmation.
- [ ] Every host PID/IPC, capability, and device grant is declared in `host_access` after user confirmation.
- [ ] No unsupported Compose feature is being silently ignored.
- [ ] `trocker init` registration is complete if this workspace was new.
- [ ] `trocker doctor` passes.
- [ ] `trocker plan` passes and shows the expected project/services.
- [ ] No Docker resources were created and no unrelated files were changed (unless the optional runtime check was requested).

## References

- [Compose hierarchy and discovery](references/compose-analysis.md)
- [Manifest, bindings, and supported subset](references/manifest-reference.md)
