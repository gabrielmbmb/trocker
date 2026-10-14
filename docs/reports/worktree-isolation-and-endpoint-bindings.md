# Worktree isolation and endpoint bindings

**Status:** Architecture proposal, not implemented behavior.

## Summary

Trocker should be a **workspace isolator with explicit endpoint bindings**, rather than a tool that searches for port numbers and rewrites them.

The central distinction is:

> Automatically discovering services and allocating ports is feasible. Automatically understanding what every configuration value means is not reliably possible.

Make discovery and isolation automatic, and express the missing application semantics in a small, one-time project configuration.

## Lessons from the existing `just wt` implementation

The reference implementation reviewed is an existing project's worktree-isolation system. These paths refer to that project, not to files currently present in Trocker:

- `scripts/wt/justfile`: lifecycle commands, secret-provider setup, and generated isolation configuration.
- `scripts/worktree_slots.py`: registry, slot allocation, resource naming, port calculation, and cleanup.
- `scripts/kennedy_native.py`: configuration for a native frontend.

It already has the right networking foundation:

```text
Container → container:  http://api:8000
Host → container:       http://localhost:20103
Browser → container:    http://localhost:20103
```

**Only published host ports change.** Container-to-container traffic keeps using service DNS and the original container port.

Its limitations for reuse are:

1. Ports come from a hardcoded service layout.
2. Slots avoid other registered slots, not unrelated processes occupying those ports. The native frontend checks its port but refuses rather than remapping.
3. The registry is repository-local. Reusing the same naming and allocation scheme in another repository could produce identical project names and port blocks.
4. Configuration propagation is application-specific. Helpers for browser URLs, backend self-URLs, and native-process environments encode knowledge about individual applications.

The fourth part should become declarative.

## 1. Model endpoints, not just ports

An endpoint should have several address representations:

| Representation | Example | Intended consumer |
| --- | --- | --- |
| Container-network | `http://api:8000` | Another container on that network |
| Host | `http://localhost:24173` | Native process, CLI, debugger |
| Public/browser | `https://api.feature-a.localhost` | Browser, redirects, generated links |

Without a proxy, the host and browser addresses can be identical. They should still be separate concepts.

**The important question is where the address will be used, not where the environment variable is stored.**

For example, a frontend container can receive both:

```text
INTERNAL_API_URL       → used by the application server inside Docker
NEXT_PUBLIC_WS_URL    → ultimately used by the user's browser
```

Looking at those values, or even knowing that both belong to a container, is insufficient to choose the correct address.

Likewise, a backend's `PUBLIC_URL` might be used to generate browser links rather than to make outbound requests.

Container addresses also require actual network reachability: a service name is not usable by an arbitrary container that does not share the appropriate network.

## 2. Declare the missing semantic information once

A hypothetical project manifest could look like this. **This is proposed Trocker syntax, not standard Compose syntax or an existing Trocker interface.**

```yaml
compose: docker-compose.yaml

endpoints:
  api:
    service: api
    port: 8000
    scheme: http

  web:
    service: web
    port: 3000
    scheme: http

bindings:
  web:
    environment:
      INTERNAL_API_URL: "{{ endpoints.api.container.url }}"
      NEXT_PUBLIC_API_URL: "{{ endpoints.web.public.url }}"
      NEXT_PUBLIC_WS_URL: "{{ endpoints.api.public.ws_url }}"

  api:
    environment:
      PUBLIC_URL: "{{ endpoints.api.public.url }}"

files:
  - template: config/development.yaml.tmpl
    output: .trocker/generated/development.yaml
```

The tool would:

1. Discover the Compose services and ports.
2. Allocate or remap published ports.
3. Resolve the endpoint references.
4. Generate a Compose override with explicit environment values.
5. Render configured files into a generated directory.
6. Mount those files or pass their paths to the application.

The example's frontend API binding deliberately uses the frontend's own public origin, assuming that the frontend proxies API requests. A frontend that contacts the API directly would instead bind to `endpoints.api.public.url`. That distinction belongs in project configuration.

For native processes, the same binding system would inject the **host** address:

```text
INTERNAL_API_URL=http://localhost:24173
```

Support a few explicit output mechanisms:

- Environment variables.
- File templates.
- Structured JSON, YAML, or TOML field patches into generated copies.
- Build arguments where necessary.

**Do not rewrite tracked configuration or arbitrary strings across the repository.**

Expose endpoint components separately, including `host`, `port`, and `url`, because many applications accept separate database host and port settings rather than a complete connection string. Credentials should remain independently supplied secrets.

## 3. Make discovery helpful, not authoritative

An initialization command could inspect:

- Compose `ports`, `expose`, networks, and `depends_on`.
- Environment values containing service names or localhost URLs.
- Known configuration conventions.
- Selected application configuration files.

Then propose bindings:

```text
Found INTERNAL_API_URL=http://api:8000
  Suggested binding: api.container.url

Found NEXT_PUBLIC_API_URL=http://localhost:8000
  Suggested binding: api.public.url
  Confirmation required.

Found CALLBACK_URL=http://localhost:8000/callback
  Is this a local endpoint or a shared authentication callback?
```

An LLM could be useful **during onboarding**, proposing this manifest from the repository. Runtime behavior should then be deterministic and reviewable.

Never assume that every occurrence of `localhost:8000` refers to the service being remapped. It might intentionally reference another stack.

Discovery cannot reliably find every address embedded in application code, stored data, generated artifacts, or external systems. The tool should explain its coverage and report unresolved candidates rather than claim complete automatic configuration.

## 4. Treat port allocation as a lifecycle problem

There are two useful allocation strategies.

### Docker-assigned ports

Compose supports publishing a container port without choosing a host port; Docker allocates an available one. The tool discovers the actual mapping after startup and injects it into consumers.

This works well for:

```text
Start database
→ discover published port
→ generate native application's configuration
→ start native application
```

It avoids implementing a custom free-port picker.

However, it is not sufficient universally: a service might need its own public URL before starting, or two services might require each other's published URLs at startup.

### Planned ports

For those cases:

1. Reuse previous assignments when available.
2. Check candidate bindings.
3. Resolve configuration before launch.
4. Attempt startup.
5. On an actual bind conflict, reallocate and regenerate affected configuration.

A registry and lock coordinate the tool's instances, but **a free-port check does not reserve a port against unrelated programs**. Docker or the application's successful bind is authoritative. Do not kill the existing listener.

The orchestrator must also avoid starting consumers against unconfirmed mappings. Cyclic public-endpoint requirements need an explicit strategy, not a naive topological startup order. Unsupported cycles should produce an actionable error rather than leave consumers pointing at unverified endpoints.

Port state should be shared across repositories and scoped to the actual publishing host and Docker daemon. Account for:

- Bind address and wildcard-address overlap.
- TCP versus UDP.
- IPv4 and IPv6.
- Remote Docker contexts: `localhost` is not universally the Docker host.
- Concurrent startup and recreation.
- Configuration changes that require dependent consumers to restart or rebuild.

Prefer loopback-only publication for local development unless the project explicitly requests broader exposure.

## 5. A local proxy makes public endpoints easier

For HTTP and WebSockets, a shared local gateway could provide:

```text
https://web.feature-a.localhost → dynamically allocated frontend port
https://api.feature-a.localhost → dynamically allocated API port

https://web.feature-b.localhost → another frontend
https://api.feature-b.localhost → another API
```

This separates:

- **Stable logical identity:** the URL applications receive.
- **Changeable transport address:** the actual published port.

The public URL can exist before the application starts. Remapping a backend port only updates the gateway route, not every consumer's configuration.

It also gives worktrees distinct browser hostnames. Different ports alone do **not** isolate cookies on `localhost`. Hostname isolation still requires applications not to deliberately share cookies across workspace domains.

The gateway needs explicit handling for:

- Its own listening-port conflicts.
- Local hostname resolution and TLS trust.
- WebSocket forwarding.
- Allowed hosts, CORS, and OAuth redirect registration.
- Verifying a destination's workspace ownership before installing its route.

It cannot automatically change a third-party OAuth allowlist. Non-HTTP protocols such as Postgres still need published ports or a suitable TCP forwarding strategy.

## 6. Proposed first version

Build a **Compose-first CLI**, with native-process support. The following commands are illustrative:

```text
trocker init                 # discover and propose bindings
trocker plan                 # explain isolation/config changes
trocker up                   # allocate, render, start, verify
trocker run web -- pnpm dev  # inject native-process bindings
trocker endpoints --json     # resolved addresses for tools/agents
trocker doctor               # unresolved references and unsafe sharing
trocker down                 # stop; preserve data by default
```

The internal pipeline would be:

```text
Compose discovery
       ↓
Workspace identity + endpoint model
       ↓
Port allocation + binding resolution
       ↓
Generated Compose/config + native-process environment
       ↓
Startup verification + endpoint registry
```

### Essential safeguards

- Give each workspace a unique project identity incorporating repository and workspace identity, not just branch name.
- Remove or namespace explicit container names.
- Detect explicitly named or external networks and volumes, shared writable bind mounts, and shared build-image tags. Require a policy for intentional sharing rather than silently mutating external resources.
- Reject unsupported `network_mode: host` isolation rather than pretending port remapping works.
- Apply generated binding values after secret-provider configuration, with documented precedence. Redact secrets from plans and protect generated files containing sensitive values.
- Distinguish build-time configuration from runtime configuration. Changing an environment variable cannot update a URL already compiled into frontend JavaScript.
- Replace Compose port lists rather than append them. A conventional override can leave the original conflicting publication in place. Compose's `!override` supports full replacement and requires Compose 2.24.4 or later.
- Preserve data by default. Workspace or branch removal should not silently authorize volume deletion.
- Track resource ownership so cleanup never removes another workspace's resources.
- Report actual verified endpoints, not merely intended assignments.

### Recommendation

Start with explicit endpoint bindings and generated configuration, plus collision-aware allocation. Add stable HTTP routing next.

The reusable abstraction is not:

> Change every reference to port 8000.

It is:

> This setting refers to this endpoint, as reached from this network context.

## References

- [Docker Compose networking](https://docs.docker.com/compose/how-tos/networking/): service DNS, container ports versus host ports, and network modes.
- [Docker Compose ports](https://docs.docker.com/reference/compose-file/services/#ports): automatic host-port allocation and binding configuration.
- [Docker Compose merge rules](https://docs.docker.com/reference/compose-file/merge/#replace-value): replacing port publications with `!override`.
