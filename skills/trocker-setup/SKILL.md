---
name: trocker-setup
description: Prepare a Git checkout to use Trocker. Use when installing the Trocker CLI, creating or validating trocker.yaml, tracing Docker Compose files through includes, overrides, and extends, declaring endpoints, bindings, and writable host paths, or checking setup readiness with trocker init, plan, and doctor.
---

# Configure Trocker for this project

Turn the current Git worktree into a usable Trocker workspace:

1. Ask before installing the Trocker CLI when `trocker` is not on `PATH`.
2. Generate `trocker.yaml` with `trocker init` when it is absent.
3. Read the complete active Compose hierarchy, not just the top-level files.
4. Add only the endpoints, bindings, and writable host paths the project actually needs.
5. Validate the result with Trocker before handing it back.

`trocker.yaml` is explicit project configuration. Trocker does not infer endpoint schemes or application bindings at runtime, so setup is where those decisions are made.

## Safety rules

- Work from the Git worktree root. `trocker.yaml` belongs there.
- Never edit Compose files, Dockerfiles, application code, `.env`, or unrelated configuration just to make Trocker accept the project. Report unsupported Compose features instead.
- Never add a `read_write_paths` entry without explicit confirmation. Each entry lets containers modify that path on the host, so show the service, host path, and container path before asking.
- Never print `.env` values, Compose environment values, secrets, generated files, or full resolved Compose configuration. List keys or use targeted queries only.
- Do not run `trocker up`, `start`, `stop`, `down`, or `run` unless the user explicitly asks for a runtime test. `init`, `plan`, and `doctor` are the normal setup checks.
- Do not bypass `trocker init` confirmation. It intentionally requires a terminal. If no PTY is available, stop and ask the user to run the exact command; do not pipe `y`, hand-write registration state, or edit Trocker's registry.
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

Run it in a real terminal or PTY. The user must see the proposal and confirm it. The generated manifest contains only the SDK-selected top-level Compose files; empty `read_write_paths: []`, `endpoints: {}`, and `bindings: {}` are expected at this stage.

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
- Build an inventory of services, declared container ports (`ports[].target` and `expose`), profiles, networks, volumes, bind mounts (writable or read-only), and unsupported features.
- Distinguish top-level files from included files. If init proposed the wrong ones, decline, correct the selection, and retry.

Do not flatten included files into `trocker.yaml`. The Compose SDK resolves and merges the hierarchy for each command.

## 4. Declare writable host paths

A bind mount is writable unless the mount declares `:ro` (short syntax) or `read_only: true` (long syntax). Trocker rejects any writable bind mount whose host source is not declared under `read_write_paths`, because that mount lets a container modify files in the checkout.

From the step 3 inventory, list every writable bind mount with its resolved host source and container target. Then:

- Declare a host source, never a container target. `read_write_paths` describes the checkout, not paths inside the container.
- A source is allowed when it equals a declared path or is nested under it. `.:/app` can only be declared as `.`; `./packages/api:/app` can be declared as `./packages/api`.
- Declare the narrowest path that covers the sources the user approves. One parent entry is fine when several approved mounts share it; do not declare `.` for a single nested mount.
- Read-only mounts need no entry. Do not declare them.
- A source outside the checkout cannot be declared at all: no Docker socket, home-directory cache, `/tmp`, or absolute path outside the worktree. Report it as a blocker and stop. Do not move, copy, or rewrite the Compose mount to work around it.

Show a table of service, host source, and container target, and ask once which paths to declare. Add entries only after the user confirms; never add a path speculatively.

```yaml
read_write_paths:
  - .
```

Confirm the result in `trocker plan`, which lists each writable mount as `Writable mount: <host source> -> <container target>`. The full contract is in [the manifest reference](references/manifest-reference.md#read_write_paths).

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
- Use one clear alias per address. YAML duplicate keys are rejected.

When the scheme, listening port, or consumer is ambiguous, ask the user or omit the endpoint. A smaller valid manifest is better than an invented one.

## 6. Add bindings only when the application consumes them

Bindings are runtime configuration overrides. Add one only after finding the actual environment key or configuration value the application reads.

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
- Do not bind values consumed only at image build time.
- Do not add `TROCKER_WORKSPACE_ID`; Trocker injects it into every managed container and native child.
- `files.<name>.path` may be referenced only in the environment of the consumer declaring that file.
- Literal text plus references is allowed. There are no functions, conditionals, shell expansion, or recursive substitutions.
- Do not put credentials in bindings. Keep secrets in their existing mechanism.
- If a required consumer/network relationship is missing, report the blocker instead of modifying Compose. A manifest with endpoints but no bindings can still be valid.

## 7. Write and validate

Update `trocker.yaml` with the same `version: 1` and the verified top-level Compose list. Prefer manifest-relative paths. Keep unrelated existing content stable.

Then validate in this order:

1. If the workspace has not been registered yet, run `trocker init` again. It validates the existing manifest and preserves any existing UUID. If you are unsure, `trocker plan` reporting that the workspace is not registered is the signal to run init.
2. Run the read-only checks:
   ```sh
   trocker doctor
   trocker plan
   ```
   `doctor` and `plan` share the same isolation, binding, and ownership checks. `plan` also shows the workspace/project name, isolated resources, selected services, writable mounts, and ports that need coordination.
3. Fix only `trocker.yaml` when validation reports a manifest problem. If validation reports an unregistered workspace or a target that is not associated, run `trocker init` and confirm. If it reports an undeclared writable bind mount, return to step 4 and confirm the path with the user; report it as a blocker when the source is outside the checkout. If it reports an ambiguous move, a missing marker, a foreign resource, or an unsupported Compose feature, stop and report the exact blocker. Do not work around it by deleting state or rewriting Compose.
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
- endpoints, bindings, and writable host paths added, with the reason for each;
- the result of `trocker init`, `trocker doctor`, and `trocker plan`;
- any assumptions, omitted ambiguous bindings, or blockers;
- the next command the user should run, normally `trocker up --detach`.

## Completion checklist

- [ ] `trocker` is installed and `trocker version` succeeds.
- [ ] `trocker.yaml` exists and `version` is `1`.
- [ ] `compose:` lists the correct top-level files in order; includes/extends are not listed.
- [ ] Every included/overridden file and active service has been reviewed.
- [ ] Every endpoint uses an active service and a declared TCP container port.
- [ ] Every binding matches a real runtime consumer and a supported address reference.
- [ ] Every writable bind mount is declared in `read_write_paths` after user confirmation, or reported as a blocker because its source is outside the checkout.
- [ ] No unsupported Compose feature is being silently ignored.
- [ ] `trocker init` registration is complete if this workspace was new.
- [ ] `trocker doctor` passes.
- [ ] `trocker plan` passes and shows the expected project/services.
- [ ] No Docker resources were created and no unrelated files were changed (unless the optional runtime check was requested).

## References

- [Compose hierarchy and discovery](references/compose-analysis.md)
- [Manifest, bindings, and supported subset](references/manifest-reference.md)
