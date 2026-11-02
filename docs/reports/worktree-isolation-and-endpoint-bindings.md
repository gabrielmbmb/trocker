# Worktree isolation and endpoint bindings

**Status:** Supporting design report, not implemented behavior. See [Trocker architecture](../architecture.md) for the main architecture reference and component responsibilities.

## Summary

Trocker should be a **workspace isolator with explicit endpoint bindings**, rather than a tool that searches for port numbers and rewrites them.

The central distinction is:

> Automatically discovering services and allocating ports is feasible. Automatically understanding what every configuration value means is not reliably possible.

Make discovery and isolation automatic, and express the missing application semantics in a small, one-time project configuration.

The proposed implementation is a daemonless Go CLI using the official Compose SDK. See [Go architecture and local state](go-architecture-and-state.md) for SDK integration, planning and execution, platform-specific state locations, concurrency, and recovery.

**Trocker must not become a speed bottleneck.** Ordinary commands do only the work needed for the selected workspace, without automatic repository-wide discovery, cleanup, or unrelated network calls. Measure incremental Trocker overhead separately from Docker/build/application wait time and establish evidence-based latency budgets; see [performance and responsiveness](go-architecture-and-state.md#9-performance-and-responsiveness).

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

The first version is **explicit-first: discovery-assisted setup, explicit runtime configuration**. Every endpoint must declare `service`, `port`, and `scheme`, even when Compose exposes only one port. Discovery helps author and validate the manifest; it does not fill in missing endpoint fields at runtime. Adding a debugger port to Compose must not change which port an existing endpoint selects.

- Keys under `endpoints` are logical aliases used in binding expressions.
- `service` names an actual service in the resolved Compose configuration.
- `port` is the service's container listening port, not its published host port. Trocker allocates or remaps the host port separately.
- `scheme` describes the service's URL protocol, such as `http` or `https`. It cannot be reliably inferred from the port number, and declaring `https` does not enable TLS on the service.
- Keys under `bindings` name the actual Compose services that receive the generated environment values.

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
    output: development.yaml
```

The tool would:

1. Load the Compose project through the official SDK and validate the explicit endpoint and binding declarations.
2. Allocate or remap published ports.
3. Resolve the endpoint references.
4. Transform the in-memory Compose project with isolated names, ownership labels, published ports, and explicit environment values.
5. Render application files only when explicitly configured, and mount them or pass their paths to the application.
6. Apply the project through the SDK and verify the actual resources and endpoint mappings.

No generated Compose override is required. In `files`, template paths are relative to the manifest; output paths are relative to Trocker's private workspace/runtime generated directory, not the checkout. Applications using only environment bindings need no generated files. The [state design](go-architecture-and-state.md#5-storage-location-and-contents) defines where these optional outputs live.

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

### Workspace identity as an application extension point

Expose `TROCKER_WORKSPACE_ID` to every managed service container and to native processes launched through `trocker run`. Make the same value available as `workspace.id` in binding expressions and generated-file templates.

The identity contract is:

- Generate a random UUID once when registering a workspace and persist it in Trocker-managed metadata, not tracked project configuration. Use its lowercase canonical string representation.
- Keep it stable across repeated initialization, stop/start, rebuilds, port reassignment, and branch switches. A supported worktree move must preserve the registered identity rather than derive a new one from its path.
- Give independently registered workspaces distinct identities, including workspaces in different repositories or clones. Neither a branch name nor a reusable allocation slot is an identity.
- Preserve it on `trocker stop` and `trocker down`, including `down --volumes`. Explicitly forgetting a workspace ends that identity's lifecycle; registering a replacement creates a new ID rather than reusing the old one. Missing metadata must not silently authorize adopting existing resources.
- Treat it as non-secret metadata suitable for names and logs, never as an authentication credential or permission to access a resource.
- Reserve the variable for Trocker: inject the registered value after secret-provider configuration and reject conflicting explicit project bindings rather than allowing the application-visible ID to diverge from resource ownership.

For example, an application that already accepts a configurable queue name could opt into namespacing through a binding. This is proposed manifest syntax:

```yaml
bindings:
  api:
    environment:
      DEMO_QUEUE_NAME: "demo-requests-{{ workspace.id }}"
```

Alternatively, application code can read `TROCKER_WORKSPACE_ID` directly. The application remains responsible for provider naming limits and for making all relevant producers and consumers use the same namespace. Supplying an ID alone does not establish isolation or create the queue.

### External services remain application-owned

Trocker isolates local stacks. External services remain shared unless the application explicitly isolates them.

A shared object-storage bucket may be intentional when object keys are namespaced, while a shared queue may cause worktrees to consume each other's jobs. Trocker cannot infer those semantics from an endpoint or environment variable.

Trocker supplies workspace identity and configuration bindings. The application decides which external resources can be shared and owns their partitioning, provisioning, permissions, and cleanup. Stopping or forgetting a workspace must not delete external resources; applications that use the ID for persistent resources must retain their own cleanup inventory.

The first version should not include an external-resource manifest, cloud provisioning, or inferred rewriting of external resource names. Project-defined lifecycle hooks can be considered later for concrete use cases, without making Trocker responsible for provider-specific semantics.

## 3. Make discovery helpful, not authoritative

`trocker init` should propose complete endpoint definitions and bindings for user confirmation. `trocker plan` should reject missing required endpoint fields and unknown service references, and report port mismatches against Compose's `ports[].target` and `expose` declarations without silently rewriting the manifest. Compose declarations are configuration evidence, not proof that the application actually listens on a port; startup verification remains necessary.

Initialization could inspect:

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

Build a **daemonless Go CLI backed by the official Compose SDK**, with foreground native-process support. The following commands are illustrative:

```text
trocker init                 # discover and propose bindings
trocker plan                 # explain isolation/config changes
trocker up                   # allocate, render, start, verify
trocker run web -- pnpm dev  # inject native-process bindings
trocker endpoints --json     # resolved addresses for tools/agents
trocker doctor               # unresolved references and unsafe sharing
trocker start                # start existing stopped containers

trocker stop                 # stop containers without removing them
trocker down                 # remove containers and networks; keep volumes
trocker down --volumes       # also remove managed named and attached anonymous volumes
```

Lifecycle names and defaults follow Docker Compose. `stop` retains containers for `start`; `down` removes them, so use `up` to recreate the stack. `start` does not apply configuration changes or reassign ports. `down --volumes` (also `-v`) explicitly requests volume deletion, never external-resource cleanup. Workspace identity survives all of these commands. See the [lifecycle contract](go-architecture-and-state.md#compose-compatible-lifecycle-commands) for retention and ownership details.

The internal pipeline would be:

```text
Compose SDK loading + explicit manifest validation
       ↓
Workspace identity + registry + Docker observations
       ↓
Isolation planning + port allocation + binding resolution
       ↓
In-memory project + optional application files + native environment
       ↓
SDK execution + inspection + verified endpoint output
```

### Essential safeguards

- Derive a unique Compose project identity from the persisted workspace ID, not a branch name or reusable port slot. Expose that same ID through `TROCKER_WORKSPACE_ID` and `workspace.id`.
- Remove or namespace explicit container names.
- Detect explicitly named or external Docker networks and volumes, shared writable bind mounts, and shared build-image tags. Require a policy for intentional sharing rather than silently mutating resources outside Trocker's ownership. This is Docker resource discovery, not discovery of application-owned cloud resources.
- Reject unsupported `network_mode: host` isolation rather than pretending port remapping works.
- Apply generated binding values after secret-provider configuration, with documented precedence. Redact secrets from plans and protect generated files containing sensitive values.
- Distinguish build-time configuration from runtime configuration. Changing an environment variable cannot update a URL already compiled into frontend JavaScript.
- Replace published-port entries in the loaded Compose project rather than appending new ones alongside the original conflicting publications. Apply the transformed model through the SDK; a generated override file is not required.
- Preserve data by default. Workspace or branch removal should not silently authorize volume deletion.
- Label owned Docker resources with the workspace ID and validate ownership before lifecycle operations or route changes. Associate generated configuration and managed native processes with the same identity. Reject stale or mismatched ownership, invalidate generated configuration when forgetting a workspace, and never treat a recycled slot or port as authority over its new owner.
- Report actual verified endpoints, not merely intended assignments. Docker is authoritative for runtime state; the persistent registry holds workspace identities and allocation metadata, not a duplicate inventory of running resources.
- Keep `status`, `endpoints`, and `doctor` observational. `stop` does not remove containers; `down` removes containers and managed networks, retaining volumes unless `--volumes` is requested. Both preserve workspace identity.

### Recommendation

Start with explicit endpoint bindings, in-memory Compose SDK transformations, collision-aware allocation, and a minimal identity/allocation registry. Generate application files only when requested. Add stable HTTP routing later.

The reusable abstraction is not:

> Change every reference to port 8000.

It is:

> This setting refers to this endpoint, as reached from this network context.

## References

- [Docker Compose networking](https://docs.docker.com/compose/how-tos/networking/): service DNS, container ports versus host ports, and network modes.
- [Docker Compose ports](https://docs.docker.com/reference/compose-file/services/#ports): automatic host-port allocation and binding configuration.
- [Docker Compose SDK](https://docs.docker.com/compose/compose-sdk/): loading and managing Compose projects directly from Go.
