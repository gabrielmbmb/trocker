# Trocker adoption gaps

Findings from trying to adopt Trocker 0.2.1 in an existing 21-service production monorepo, and the subset of the Compose surface its dev workflow depends on. Written against 0.2.1 source (`internal/compose`, `internal/plan`, `internal/state`, `internal/cli`); line numbers drift between releases, function names are the stable reference.

The adopter is a realistic worst case rather than a synthetic one: a FastAPI backend with four worker roles, a Playwright-based screen-sharing agent, a Node mapping service, three Next.js apps, and LocalStack/Datadog/Doppler dev tooling. One top-level aggregator Compose file, eight files reached through `include`, 21 services, five profile-gated.

Sections are ordered by whether adoption is possible at all: §1 rejects the Compose file, §2–§3 make daily use costly, §4–§5 are correctness and clarity of the tool itself. §6 proposes an order.

## Resolved since this report

| Gap | Resolution |
| --- | --- |
| §1.1 writable bind mounts | Declared per manifest in `read_write_paths`; undeclared writable mounts still fail. |
| §1.2 Docker socket | Declared per service in `docker_socket_services`, with a dedicated diagnostic. |
| §1.3 `pid`/`ipc`/`cap_add`/`devices` | Declared per service in `host_access`; `privileged`, `uts`, `cgroup`, and `device_cgroup_rules` remain rejected. |
| §1 `build.additional_contexts` | Declared per manifest in `build_contexts`; only in-checkout paths, and `service:`/remote contexts stay rejected. |
| §1.1 writable paths outside the checkout | Declared per manifest in `shared_write_paths` with `~/` expansion; never created by Docker, never owned or removed by Trocker. |
| §3.2, §3.3, §3.8 workspace removal | Registration now tracks the worktree, not the stack. `trocker release [WORKSPACE] [--volumes]` stops what a workspace still owns from any directory and then deletes its registration; `init` sweeps registrations whose worktree Git no longer records and which own nothing; `ls --all` reports the rest as `missing`. |
| §3.1 non-interactive registration | `init --dry-run` reports a digest and writes nothing; `init --confirm <digest>` registers only while the proposal still matches. The digest covers the effective manifest, the Docker target, creation, and relocation, and excludes the checkout path and identity, so one approval covers every worktree with that configuration. |

Re-verified against the adopter with a rebuilt binary: the full declaration set loads and builds with default profiles (16 active services) and with all five profiles enabled (21 services). Removing each declaration on its own is still rejected, naming the service and the path — `read_write_paths`, `shared_write_paths`, `docker_socket_services`, `host_access`, and, once `kennedy-frontend` is active, `build_contexts`.

Everything below still applies. §1.4, §1.5, §2, §3.4–§3.7, §4, and §5 are unchanged by the resolutions above.

One correction to the inventory: only `kennedy-frontend` declares `additional_contexts`, and both of its values are inside the checkout; `website` and `swag` do not use it. The writable `~/.aws/sso/cache` mount used by `apollo`, `hubble`, `kennedy-api`, `kennedy-worker`, and `asset-sentinel-worker` — the residual §1.1 case, since `read_write_paths` stops at the checkout — is now expressible as `shared_write_paths`.

## 1. Blockers: unsupported Compose constructs

The deny-list in `internal/compose/load.go` (`LoadForPlan`) plus the checks in `internal/plan/project.go` (`isolateService`) reject these before any mutation. At the time of the report, 13 of 21 adopter services were rejected by one of the first four rows; the first three are now opt-in grants.

| Gap | Enforced at | Adopter evidence | Notes |
| --- | --- | --- | --- |
| Writable bind mounts | `isolateService`, `types.VolumeTypeBind` case | Source mounts on 10 services (`.:/workspace/packages/<service>`, `.:/app`) | Resolved: `read_write_paths` |
| Docker socket as a bind mount | Same check as above | `localstack`, `dozzle`, `datadog-agent` | Resolved: `docker_socket_services` |
| `pid: host` (namespace access) | `LoadForPlan` deny-list, `services.*.pid` | `datadog-agent`, which the aggregator includes unconditionally | Resolved: `host_access` |
| `build.additional_contexts` | `LoadForPlan` deny-list | Frontend image builds against sibling workspace packages | Resolved: `build_contexts` |
| `deploy.*` rejected as a whole key | `LoadForPlan` deny-list, `services.*.deploy` | Not used here | See §1.4 |
| Explicit `networks.*.name` / `volumes.*.name` | `LoadForPlan` deny-list | Not used here | See §1.5 |
| `secrets`, `configs`, `models`, `jobs`, `provider`, `hooks`, `develop` | `LoadForPlan` deny-list | `develop` would be the obvious fallback for §1.1 | Rejecting `develop` removes the only mount-free hot-reload path |

### 1.1 Writable bind mounts

`isolateService` rejects any `types.VolumeTypeBind` mount without `ReadOnly`. This is the single largest adoption barrier, because writable source mounts are how Docker-based development does hot reload, and read-only mounts are not a substitute: the container cannot write the source tree at all.

The error text is `writable bind mounts require an explicit sharing policy`, which implies a policy mechanism that does not exist in 0.2.1. Either implement it or make the message and the README say "unsupported" consistently.

What an adopter needs is narrower than "mount anything":

- A per-mount or per-manifest opt-in naming the host paths being shared, so the decision is explicit and reviewable rather than a blanket flag.
- Guardrails that keep the current safety property: refuse overlapping writable mounts with generated-file targets, refuse writable mounts on the Docker socket (see §1.2), and keep the existing refusal of writable mounts that carry ownership labels.

Without a hot-reload story, the gap also blocks the second-order workflow: services are usually also the test containers (`docker exec … pytest`), so the rejection takes out both "edit and see it" and "run the suite".

### 1.2 Docker socket mounts

The deny-list has a dedicated concept for the socket via `services.*.use_api_socket`, but a plain `/var/run/docker.sock:/var/run/docker.sock` mount never reaches it: `isolateService` catches it first as a writable bind mount and reports the generic bind-mount error. The diagnostic misdirects, and there is no supported way to express "this service legitimately needs the Docker API".

Three distinct legitimate cases exist in the adopter: LocalStack (needs the API to run sibling services), a log viewer, and the Datadog agent (needs container metadata). Treating all three as bind-mount violations means the only workaround is removing observability and AWS emulation, which is not an option for a real dev stack.

### 1.3 Namespace access

`pid: host` is rejected through the `services.*.pid` deny-list entry with no opt-in. This is correct as a default and wrong as an absolute, because `pid: host` on a metrics/logging agent is a standard, contained use. Same for `cap_add`, `devices`, and `ipc` — the security posture is right, the absence of an escape hatch is the gap.

### 1.4 `deploy` rejected wholesale

The deny-list rejects `services.*.deploy` entirely, so a project that only sets `deploy.resources.limits` — which Compose honors locally for `docker compose up` in recent versions, and which many teams set for parity with their orchestrator — cannot load. Consider rejecting the replication-bearing subkeys (`deploy.replicas`, `deploy.placement`, `deploy.update_config`) and accepting the resource and restart subkeys.

### 1.5 Explicit resource names

`networks.*.name` and `volumes.*.name` are rejected. Naming a volume `${COMPOSE_PROJECT_NAME}_data` is common practice for readability and appears in most published Compose examples. Trocker already rewrites project-scoped volume names (`project.Name + "_" + name` in `isolateVolumes`), so the strictness buys less than it costs. At minimum, the error should say why the explicit name is a problem and what to remove.

## 2. Port model

The model is coherent — ephemeral (`Published: "0"`) publications on loopback, remembered preferences for reuse, coordinated pre-start allocation for container bindings — but it has no room for a consumer that must know the port before the stack starts.

### 2.1 No declarable host port

`type Endpoint` in `internal/config/manifest.go` has `service`, `port` (container), and `scheme`. `isolatedPorts` unconditionally rewrites every publication to `Published: "0"`, `HostIP: "127.0.0.1"`. There is no manifest field, flag, or environment variable that requests a specific host port.

### 2.2 `plan` cannot reveal ports

`resolveHost` in `internal/plan/ports.go` returns `<allocated-at-startup>` whenever `Options.Preview` is set, which is how `plan` and `doctor` render host-safe bindings. A read-only check therefore validates that a reference parses and that the alias exists, but cannot prove the reference will resolve. Allocation failures surface only at `up`. For a tool whose main promise is "check before you mutate", this is the weaker half of that promise.

### 2.3 Consumers that need a pre-registered port

This is the real cost. Several classes of consumer need the *value* of a port outside this machine, before startup:

- OAuth/OIDC redirect URIs registered in a third-party dashboard. Dynamic ports cannot be pre-registered, so hosted login is unusable even though a local emulator works.
- CORS allowlists compiled into another service's configuration.
- WebAuthn relying-party origins.
- Third-party webhook callbacks.

Remembered preferences make the port *usually* stable, which is worse than deterministic: it works in development and breaks at the least convenient time. `docs/architecture.md` (`Port allocation and locking`) already says a probe is not a reservation against unrelated programs.

Suggested shape: an optional `endpoints.<alias>.host_port`, allocated before startup, failing loudly when unavailable instead of silently falling back to an ephemeral candidate. That preserves isolation (the port still belongs to one workspace) and gives the class of consumers above something to depend on.

### 2.4 Native bindings cannot drive allocation

`RequiredPorts` deliberately excludes native bindings, because `run` consumes observations from an already-running stack. Reasonable for `run`, but it means the "run the app natively, keep dependencies in Docker" workflow can never be the thing that pins a port — it always inherits whatever `up` happened to assign.

## 3. Workflow gaps for agent-driven worktrees

The adopter's worktree lifecycle is agent-driven: an agent creates a worktree per task, works, and discards it. Trocker's registration model assumes a human is present for each one.

### 3.1 Registration requires a terminal, per worktree

Resolved: `init --dry-run` and `init --confirm <digest>` register without a terminal. The digest covers the effective manifest, the Docker target, whether the manifest would be created, and whether a moved checkout would be relinked; it excludes the checkout path and the workspace identity, so one approval covers every worktree that shares the configuration, while editing the manifest or switching daemons requires a new approval. Approving is still the user's decision: an agent may pass a digest the user approved, never choose one itself.

### 3.2 No way to unregister a workspace

`internal/state` has no delete or release path, and the CLI has no `unregister`/`forget`/`prune` command (all three exit 2 as unknown commands in 0.2.1). Registration is permanent:

- Every discarded worktree leaves registry and Git-marker state behind.
- There is no support command to clean it up.
- The only recovery described is for an *ambiguous move*, which is a different problem.
- Accumulated registrations make `ls --all` progressively less useful.

Suggested shape: `trocker forget` that refuses unless the workspace owns no containers, networks, or retained volumes, and reports exactly what still holds it. The existing ownership checks already provide the evidence; only the command is missing.

### 3.3 No workspace sweep

There is nothing that reaps stacks whose worktree no longer exists. `ls` observes, it does not clean. Combined with §3.2, the failure mode is monotonically growing state that only manual `docker` work clears.

### 3.4 No `logs`

`up` without `--detach` streams logs but holds the workspace mutation lock until the session ends, so tailing one service blocks every other mutating command for that workspace. There is no way to read logs from a detached stack. A `logs [SERVICE...]` command needs no allocation or locking and would remove a very routine dependency on raw `docker`.

### 3.5 No `exec` or `ps`, and no documented way to address containers

`container_name` is removed by design. `status` does print the resulting container names, but it has no `--json`, and no command exposes the Compose project name as a documented result. `endpoints --json` reports `workspace_id`, from which the project name (`trocker-<uuid>` minus hyphens) can be *derived*, but deriving it is not a contract. Every test recipe in the adopter resolves a container by name, so this is the difference between `docker exec <name> pytest` and a label-filtered `docker ps` dance.

Suggested shape: `trocker ps --json` (container names, services, states, health) and either the project name in `endpoints --json` or a `trocker project` result. Both are read-only observations of state Trocker already has.

### 3.6 Profiles are environment-only

There is no reference to `Profiles` anywhere in `internal/`, and no `--profile` flag on any command: profile-gated services appear purely because `COMPOSE_PROFILES` happened to be set in the invoking shell. The manifest cannot declare which services it wants, so `plan` in a shell without the variable fails with `endpoint service is not selected`, which points at the manifest rather than at the environment. A manifest-level service selection, or at least a `--profile` pass-through on `plan`/`up`/`down`, would make the failure self-explanatory.

This is not only cosmetic. `ValidateProject` requires every `endpoints` and `bindings` service to be active, so a checked-in manifest that declares a host address for a profile-gated app fails validation unless the profile happens to be enabled. In the adopter, the three Next.js apps that most need browser-facing addresses are all profile-gated, so `endpoints` and `bindings` cannot cover them without making the manifest depend on the invoking shell. The same `trocker.yaml` then passes in one terminal and fails in another.

### 3.7 New-worktree state seeding has no hook

A fresh worktree of a real project is not runnable: per-package `.env` files, secret-manager scoping (anything keyed by directory), and dev-only fixture files are all missing. Trocker owns workspace creation but has no hook between "registered" and "up", so every adopter rebuilds this step outside the tool — which is exactly the plumbing Trocker exists to remove. This is arguably out of scope, but it is the step that decides whether a new worktree is usable, and doing it twice (once in Trocker, once in project scripts) is the current outcome.

Interpolation inputs are part of the same hole. The adopter's Compose file interpolates `${DD_AGENT_VERSION:?run the dev stack via just, which reads datadog-agent.version}`; loading without it fails before any policy check with `cannot load Compose configuration`, and there is no way to declare a derived value in the manifest. Adoption therefore requires an external wrapper that exports project-specific variables before every Trocker command, which is the pattern `trocker` is meant to replace.

### 3.8 No cross-workspace teardown

`down` and `stop` act on the current workspace only; there is no equivalent of "stop the stack in worktree N". Cleaning up after an agent that died mid-task therefore requires the registry path and manual Docker work.

## 4. Isolation coverage gaps

The deny-list is explicit and finite: `LoadForPlan` lists a fixed pattern set, `host_access` covers namespace/capability/device grants, and `isolateService` covers mounts, ports, networks, scale, and labels. Anything not in those two places passes through to Docker verbatim.

Service keys that reach Docker untouched today, verified absent from the deny-list, `host_access`, and `isolateService`: `userns_mode`, `security_opt`, `sysctls`, `cgroup_parent`, `runtime`, `isolation`, `group_add`, `tmpfs` options, `hostname`, `platform`, `pull_policy`, `init`, `stop_grace_period`.

Most are harmless. `userns_mode`, `security_opt`, `sysctls`, and `cgroup_parent` are isolation-relevant and currently unclassified, which contradicts the README's framing that unsupported features are rejected rather than silently accepted.

A hand-maintained list rots against Compose's schema. Consider a schema-completeness test: enumerate `compose-go`'s `ServiceConfig` fields and assert each one is classified as isolated, passed through, or rejected. That turns an implicit allow-list into a reviewed contract and makes a new upstream field a test failure instead of a silent gap.

## 5. Diagnostics, docs, and tooling

- **The isolation policy is unnamed in user-facing output.** `LoadForPlan` returns `not supported by the initial isolation policy` with a field path. Neither the README nor a doc enumerates the 33 patterns, so the message tells the user that a rule exists but not which rules do. Generating a rejected-paths table from the pattern slice would keep it true by construction.
- **The writable-bind error promises a policy that does not exist** (§1.1). Either implement the sharing policy or say "unsupported" and link the pattern table.
- **`plan` prints `<allocated-at-startup>`** (§2.2). It is honest, but it appears where users expect a URL. Pair it with a one-line note that host addresses are final only after `up`.
- **`status` lists only containers.** Networks and volumes are checked for ownership but never displayed, so a user diagnosing retained resources has to leave the tool.
- **No machine-readable `plan`.** Setup automation can read `endpoints --json`, but there is no `plan --json`, so anything that wants to verify configuration in a pipeline must scrape human output.
- **`doctor` and `plan` share every check**, which reads as one command with two names. If they are meant to differ (depth, cost, target reachability), the difference is not visible.

## 6. Suggested unblock order

Ordered by services unblocked per unit of design risk, not by implementation cost.

| Order | Change | Unblocks | Risk to preserve |
| --- | --- | --- | --- |
| ~~1~~ | ~~Sharing policy for writable bind mounts (§1.1)~~ | Resolved: `read_write_paths` | |
| ~~2~~ | ~~Socket-mount opt-in with a dedicated error (§1.2)~~ | Resolved: `docker_socket_services` | |
| ~~3~~ | ~~`unregister`/`forget` + sweep (§3.2, §3.3)~~ | Resolved: `trocker release` + `init` sweep | |
| ~~4~~ | ~~Digest-based non-interactive `init` (§3.1)~~ | Resolved: `init --dry-run` + `init --confirm <digest>` | |
| 5 | Declarable host port (§2.1–§2.3) | OAuth/OIDC, CORS, WebAuthn, webhooks | Still one owner per port; fail, never fall back |
| 6 | `ps --json` / `logs` (§3.4, §3.5) | Test and debugging recipes that currently shell out | Read-only, no locking |
| 7 | Deny-list completeness test (§4) | Confidence in the isolation claim | None — it is a test |
| 8 | `--profile` pass-through, `deploy` subkey granularity, explicit-name rejection wording (§1.4, §1.5, §3.6) | Projects with ordinary Compose hygiene | None |

Item 4 is now the highest-ranked open item, and items 5–8 are what make adoption pleasant.

## Appendix: adopter inventory

21 services across one aggregator plus eight included files. Profile-gated: `kennedy-frontend`, `website`, `swag`, `n8n`, and `workos-emulate` (through the `workos-local` profile).

| Service | Rejected by | Notes |
| --- | --- | --- |
| `kennedy-api`, `kennedy-worker`, `asset-sentinel-worker` | writable bind mounts | Source mount plus a writable SSO token cache |
| `apollo`, `apollo-voice-chat` | writable bind mounts | `apollo-voice-chat` also uses `ports: !reset []` |
| `hubble` | writable bind mounts | Source mount plus a named `node_modules` volume |
| `hubble-router` | writable bind mounts | Two narrow source mounts |
| `kennedy-frontend`, `website`, `swag` | writable bind mounts, `additional_contexts` | All three profile-gated |
| `localstack` | Docker socket | A backend service waits on its health |
| `dozzle` | Docker socket | |
| `datadog-agent` | Docker socket, `pid: host` | Included unconditionally; also needs `/proc` and cgroup mounts |
| `workos-emulate` | — | Read-only bind mount, accepted |
| `test-pages` | — | Read-only bind mount, accepted |
| `hubble-gateway` | — | Read-only bind mount, accepted |
| `postgres`, `redis`, `arq-ui`, `toxiproxy`, `n8n` | — | 8 of 21 services load cleanly; none of them is an application |
