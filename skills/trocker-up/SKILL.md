---
name: trocker-up
description: Start an isolated development environment for the current Git worktree using Trocker. Use when the project already has trocker.yaml and the user wants to bring up the worktree's stack.
---

# Start this worktree's environment

The project already has Trocker configured. Use its existing `trocker.yaml`; do not recreate configuration or manually assign project names or ports.

1. Confirm you are in the intended Git worktree with `git rev-parse --show-toplevel`, then work from that root. Check that `trocker.yaml` exists. If it is missing, use the `trocker-setup` skill instead.
2. Run `trocker plan`. If the worktree or selected local Docker target needs registration, run `trocker init`, then retry the plan. Confirmation is the user's decision: let them answer the terminal prompt, or show them `trocker init --dry-run` and use their approved digest with `trocker init --confirm <digest>`. Never pick the digest yourself, pipe confirmation, or edit registration state.
3. Once the plan passes, start the environment:
   ```sh
   trocker up --detach
   ```
   Add `--build` when the task requires rebuilding images. Select services only when requested, including their binding dependencies.
4. Check the result:
   ```sh
   trocker status
   trocker endpoints --json
   ```
   Report the running services, published addresses, and any failures. Use observed addresses, not guessed or remembered ports. A published endpoint alone does not prove application readiness.

   For runtime debugging, use `trocker ps`, `trocker logs [SERVICE...]`, and `trocker exec SERVICE COMMAND...`. They resolve the current worktree's containers by Compose service name; do not address generated container names or fall back to raw `docker`. Use `trocker ps --json` when a script needs the project or container names.

## Safeguards

- Use Trocker, not direct Compose commands, to manage this stack.
- Do not modify Compose files, application settings, or `trocker.yaml` to bypass startup errors. Report configuration, ownership, registration, or unsupported-feature blockers.
- Never print secrets or resolved environment values.
- Leave the environment running. Do not tear down resources, delete volumes, or reset Trocker state unless explicitly requested.
