# Contract fixtures

These fixtures define acceptance cases for [the initial contracts](../../initial-contracts.md).
`internal/compose/load_test.go` loads these directly through the real offline SDK.
`internal/config/manifest_test.go` covers decoding and semantic validation separately.
Run `just check`; these tests do not require Docker. Structural YAML errors expose
safe locations, not raw library messages that may contain secret values.

Each manifest resolves `compose.yaml` relative to this directory. The Compose
project is loading-only: its declared ports intentionally do not guarantee that
the image listens on them. Do not use it as a runtime readiness fixture.

| Manifest | Loader outcome | Subsequent behavior |
| --- | --- | --- |
| `minimal.yaml` | Valid | No endpoint bindings. |
| `bindings.yaml` | Valid | Container binding is plan-compatible; native host binding requires observation. |
| `native-files.yaml` | Valid; template input/syntax checked without rendering | Native JSON is rendered only by `run`, after observing the API. |
| `container-files.yaml` | Valid; template input/syntax checked without rendering | Container JSON is rendered by `up` and delivered through a read-only Compose config. |
| `host-before-start.yaml` | Valid | Read-only planning reports allocation requirements; startup coordinates and verifies the host binding. |
| `unsupported-version.yaml` | Unsupported version at `version` | No effects. |
| `duplicate-key.yaml` | YAML structure error with location | No effects. |
| `unknown-field.yaml` | YAML structure error with location | No effects. |
| `invalid-reference.yaml` | Unknown endpoint in `bindings.web.environment.API_URL` | Do not echo the binding value. |
| `undeclared-port.yaml` | Undeclared port at `endpoints.api.port` | Do not change Compose declarations. |

The tests additionally cover required fields, multiple documents, invalid shapes,
empty paths, invalid port ranges, unsupported schemes, environment names, malformed
delimiters, reserved identity bindings, and native container-address references.
`internal/config/project_test.go` tests service/port validation directly, including
container-versus-published ports, TCP/UDP mismatches, and unchanged project inputs.
SDK loading tests retain real-loader checks for interpolation, `.env`, overrides,
includes, port normalization, profiles, cancellation, and unchanged input files on
success and failure. YAML library internals are not tested separately.
