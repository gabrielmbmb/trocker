# Generated application files

**Status: native delivery (10a) and container delivery (10b) are implemented on Linux/macOS hosts.** Native files use `bindings.<service>.native.files`; container files use `bindings.<service>.files` with required `target`. The demo worker consumes both: `examples/native-config` for native JSON and `examples/container-config` for its container API-address file. This feature does not introduce a gateway, secret manager, build-time templating, or background supervisor.

## 1. Scope and delivery order

Support applications that need endpoint/workspace configuration in a file rather than individual environment variables.

1. **10a: native consumers.** Generate private files before `run` and expose their absolute paths through explicit native environment bindings. This builds on the existing native workflow without Docker mount semantics.
2. **10b: container consumers.** Deliver generated files read-only through the Compose SDK, coordinate pre-start host ports when needed, and recreate affected consumers when file content changes. File-backed Compose configs provide read-only mounts; private host directories and readable mounted files support non-root consumers.

Do not add automatic reload, arbitrary file patching, source-file rewriting, template downloads, or implicit command arguments. Native execution remains Linux/macOS-only initially.

## 2. Manifest syntax

Keep files beside the environment bindings of their consumer. Container and native definitions remain separate, just like their environment bindings.

```yaml
version: 1
compose: [compose.yaml]

endpoints:
  api:
    service: api
    port: 8000
    scheme: http

bindings:
  web:
    native:
      files:
        app_config:
          template: .trocker/templates/app.json.tmpl
          output: app.json
          format: json
          values:
            api_url: "{{ endpoints.api.host.url }}"
            workspace_id: "{{ workspace.id }}"
      environment:
        APP_CONFIG: "{{ files.app_config.path }}"
```

The container counterpart:

```yaml
bindings:
  web:
    files:
      app_config:
        template: .trocker/templates/app.json.tmpl
        output: app.json
        format: json
        target: /run/trocker/app.json
        values:
          api_url: "{{ endpoints.api.container.url }}"
          workspace_id: "{{ workspace.id }}"
    environment:
      APP_CONFIG: "{{ files.app_config.path }}"
```

Fields:

| Field | Rule |
| --- | --- |
| File identifier | Same restricted identifier syntax as endpoint aliases. Unique in its consumer scope. |
| `template` | Required path relative to the manifest directory. Must resolve to a regular file inside that directory tree; no absolute paths or symlink escapes. |
| `output` | Required filename, not a path. No separators, colons, `.` or `..`. Unique among that consumer's files, including case-insensitive duplicates. This names a file under Trocker's private state, never the checkout. |
| `format` | Required `json` or `text` initially. JSON output must be one valid JSON value; text is emitted as rendered. Other format validation waits for a concrete need. |
| `values` | Optional string map using the existing binding-reference grammar. Keys match `[A-Za-z_][A-Za-z0-9_]*`; values follow the YAML library's string conversion rules. Only explicitly supplied values are exposed to the template. |
| `target` | Required only for container files: an absolute, clean, ordinary container file path. Reject root/special-filesystem targets and overlapping generated/Compose mounts. Native files cannot set it. |

`files.<identifier>.path` is allowed only in the matching consumer's environment bindings:

- Native: the generated absolute host path.
- Container: the declared container `target`, never the private host source path.
- Unknown file references and cross-scope references fail validation.
- Native files must have an explicit path reference in their native environment; otherwise there is no supported delivery mechanism.
- File `values` cannot reference other generated files. This avoids file-to-file ordering and self-referential output paths.

The child argv stays unchanged. An application can read `APP_CONFIG` directly. If it accepts only a flag, the user can explicitly opt into a shell wrapper:

```sh
trocker run web -- sh -c 'exec myapp --config "$APP_CONFIG"'
```

Trocker does not interpolate the command line or append `--config` itself.

## 3. Template contract

Use Go's standard-library `text/template`, not `html/template` or Sprig.

`.trocker/templates/app.json.tmpl`:

```gotemplate
{
  "apiUrl": {{ json .api_url }},
  "workspaceId": {{ json .workspace_id }}
}
```

- The template root is the explicit `values` map after binding resolution. There is no ambient `.Env`, full Compose model, registry, or filesystem object.
- Provide one custom helper initially: `json`, implemented through `encoding/json`, returning a JSON-encoded value. Use it for strings rather than inserting values between manually written quotes.
- Set `missingkey=error` for ordinary missing-key lookups. Follow Go template semantics; this is not a claim that every built-in operation, such as `index`, rejects absent map entries.
- Standard Go template control flow and built-ins remain available. No custom environment, file-read, network, or command-execution functions.
- `values` are strings in the initial schema. Numeric fields require intentional rendering; do not silently infer YAML scalar types or introduce conversion helpers without a concrete consumer.
- Initially cap each template input and each rendered file at 1 MiB. These are I/O bounds, not a sandbox or execution-time guarantee. Templates are trusted application configuration.
- Report the file identifier and safe failure category, optionally a safe line number. Never print raw template execution errors, resolved values, or rendered content.
- After rendering, validate JSON before publishing any files. Preserve rendered bytes rather than reformatting them.

Secrets are not implicitly imported from the environment. If an application needs secret-to-file inputs, define an explicit input contract for that concrete application rather than exposing the entire inherited environment.

## 4. Resolution and command behavior

File values participate in the same endpoint validation as environment values:

- Native values can reference `workspace.id` and observed host endpoint addresses. Referenced dependencies must already be running; missing publications prevent rendering and launch.
- Container values can use container addresses or host addresses. Host `port`/`url` references must contribute to step 8's pre-start allocation requirements, even when no environment binding references that endpoint.
- A bind-conflict retry must rerender affected container files with the new allocation, then pass the updated consumer configuration through SDK convergence.
- An unselected container endpoint service still needs selection or a declared Compose dependency. Files do not invent a new readiness graph.

| Command | Behavior |
| --- | --- |
| `init` | Validate declarations and template inputs; do not create rendered output. |
| `plan` / `doctor` | Check paths, syntax, references, mount conflicts, and allocation requirements without writing or claiming ports. Explain checks deferred until rendering; do not claim unresolved JSON has been validated. Never show resolved/rendered secrets. |
| `run` | Resolve native inputs, render/validate the complete selected file set, publish a generation, resolve file-path environment bindings, then launch. No workspace/registry lock spans the child lifetime. |
| `up` | Allocate, render/validate selected container files, publish files, inject read-only delivery and file paths, then converge through Compose. Verify actual bindings before success. |
| `start` | Use the existing container configuration and file generation. Do not rerender or apply edits; direct changes to `up`. |
| `stop` / `status` / `endpoints` | Never rerender or rewrite files. |
| `down` | Preserve files still needed by retained consumers; volume deletion is not authorization to delete native configuration files. |

Application reload and application-specific schema validation are not inferred from successful rendering.

## 5. Storage, concurrency, and publication

Files use this private tree (`mode` is `native` or `container`):

```text
<state>/generated/<workspace-id>/<target-key-or-local>/<mode>/<service-key>/
    current.json
    <generation-id>/
        app.json
```

- `mode` separates native/container scopes. `target-key` is `target-` plus SHA-256 of the verified daemon identity; `service-key` is `service-` plus SHA-256 of the service name, avoiding unsafe/case-colliding path components. Any endpoint reference in the native consumer selects its target scope; literal/UUID-only inputs use `local` without contacting Docker. These hashes contain identity metadata, not rendered secrets.
- Generation IDs are opaque, not secret-derived hashes exposed in paths or labels. A small private `current.json` may identify the most recently prepared generation; it is a cache hint, not runtime truth. Persist no resolved values in it or the registry.
- Render and validate the entire set before making it selectable. Publish through same-filesystem staging and atomic rename; failed rendering must not leave a partially published set.
- Reuse the current complete generation when its filenames and rendered bytes match; older retained generations are not searched. Template edits that produce identical output need not rewrite files or recreate consumers.
- Never replace the contents of an in-use generation. Changed bytes produce a new generation so an existing process keeps its original snapshot.
- Serialize only the generation comparison/publication boundary for a consumer. Do not hold a global registry lock while rendering, doing SDK work, or running a child.
- Use anchored filesystem operations for confinement, not a check-then-write sequence vulnerable to symlink replacement. Reject unsafe preexisting state paths rather than repairing them silently.
- All directories are `0700`; metadata and native files are `0600`. Container files are `0444` within that private tree, individually mounted read-only. Do not print file contents or loosen directory permissions.

`internal/generated` handles bounded input reading, parsing, explicit value resolution, rendering, and JSON validation. `internal/state/generated.go` uses `os.Root`-anchored filesystem operations, a consumer-scoped OS lock, exclusive private staging, synced file writes, and atomic directory/index renames. Bookkeeping is only a versioned current-generation pointer; it is not a general operation journal or background cache service.

## 6. Container delivery and permission policy

Pinned Compose v5.6.1 `buildContainerConfigMounts` turns file-backed configs into read-only bind mounts. Its inline `Content` path instead copies a tar archive into the container and rejects read-only root filesystems; it is not read-only mount delivery. Trocker therefore supplies `types.ConfigObjConfig.File` and service config references, leaving mount creation and convergence to Compose. No custom tar/copy or mount orchestrator is added.

Compose's service hash includes config references, not top-level file contents. Each generated reference includes the service, opaque generation UUID, and file identifier. Changed bytes select a new immutable source and reference, causing SDK recreation; unchanged bytes reuse the current reference. No secret-derived content digest is exposed in labels or names.

Container files are explicitly `0444` beneath `0700` host directories, with private metadata. Other ordinary host users cannot traverse the private tree, while any UID in the selected container can read its individual mounted file. Mounts are read-only even for container root; this is not per-UID secrecy within a container. Do not mount the host generation directory or make it public. The daemon must be able to access the private tree. File-backed config `uid`/`gid` settings are not used or relied upon.

Supported hosts are Linux/macOS; Windows-host publication is rejected pending an ACL policy. Remote targets remain unsupported. Declared generated/Compose mount overlaps and special filesystem or Docker-managed hostname/DNS targets are rejected. Source templates are never mounted or modified. Real tests run a non-root BusyBox HTTP consumer with a read-only root filesystem, inspect the read-only bind, and verify content after recreation and port-conflict retry.

## 7. Retention and failures

- Render/validation failure: launch nothing new and leave previous generations intact.
- SDK startup failure or cancellation: retain any generation that a partial or stopped container may still reference. Do not roll back by deleting mounted files.
- Concurrent native invocations may use different immutable generations without changing each other's inputs.
- **No automatic native-generation garbage collection in 10a.** A parent crash can release its OS lock while a child survives. A missing lock or PID is not proof that a file is unused. Retain published generations until an explicit, safe cleanup contract exists.
- Remove only unpublished staging owned by the current operation when it is safe to do so. Crash leftovers remain private and must never be mistaken for complete generations.
- Container generations are also retained without automatic cleanup. Any future cleanup must inspect owned containers including stopped containers and retain files on uncertainty or daemon unavailability. Do not scan unrelated workspaces or use volume pruning as file cleanup.
- Identity forgetting/recovery is already deferred; this feature must not quietly introduce it. Deleting source templates is never part of generated-file cleanup.

Retention means old secrets may remain in private state after configuration changes. Document this rather than promising automatic secure erasure.

## 8. Acceptance tests and implementation gate

Use a tiny real application/helper that opens the supplied configuration path, parses its JSON, and contacts an owned test endpoint—not tests that only compare renderer internals.

- Correct endpoint/UUID values; quotes, backslashes, Unicode, and newlines are JSON-encoded correctly.
- Missing ordinary keys, invalid JSON, invalid templates, and size limits fail before launch without leaking values.
- Unknown/cross-scope file references, duplicate output names, file-to-file references, and mount collisions fail validation.
- Input resolution works from a checkout subdirectory; absolute paths, traversal, and source/output symlink escapes fail without outside reads/writes.
- A failed multi-file render publishes no partial set; injected write/rename failures preserve the previous generation.
- Identical output reuses files; changed output leaves existing native consumers' snapshots unchanged.
- Real native environment-path delivery works with spaces in state paths, no argv rewriting, and no lifetime global/workspace lock.
- Real container delivery in 10b verifies read-only access, supported non-root access, content-triggered recreation, stopped-container retention, and actual file contents after a port-conflict retry.
- Cancellation, crash leftovers, concurrent launches, and target/workspace separation preserve data and ownership.
- No automatic cleanup deletes uncertain native generations, mounted container generations, or source files.

10a tests exercise real native JSON consumers, an owned Docker HTTP endpoint, filesystem failures and confinement, concurrent subprocess publication, SIGKILL/retry, and private immutable generations. 10b tests cover file-only allocation, selection and mount safety, host permission/scope isolation, non-root read-only access, unchanged startup, content-triggered recreation, stopped-container retention, and rerendering on a real port-bind race. Keep unsupported behavior explicit and run the relevant unit/race/integration/platform checks.
