# ADR 0001: Run Claude pipeline phases through the isolated `claude` CLI

- **Status:** Accepted
- **Date:** 2026-10-21
- **Beads:** `acf-23` (feature), `acf-28` (brainstorm), `acf-34` (stress-test)
- **Spec:** `.internal/specs/2026-10-21-runtime-phase-adapters-design.md`

## Context

Pipeline phases (Producer, Dev, QA, Review) could only run through the Copilot adapter. The factory
needs other runtimes, starting with Claude, without weakening the phase controls the Copilot
adapter established: no shell, Dev-only writes, hooks disabled, sealed inputs and profiles, and
verdicts bound to the fingerprint.

There were three ways to drive Claude:
- the `claude` CLI as a supervised child process;
- the Claude Agent SDK, whose `canUseTool` callback approves file access in code;
- the Messages API called directly from Go, with factory-owned tools.

## Decision

1. Extract a runtime-neutral phase runner from the Copilot adapter; Copilot and Claude are drivers behind
   a small `Driver` interface. Policy v2 adds `runtime`, neutral `grants`, and an optional `model`.
2. Drive Claude through the **`claude` CLI** using **subscription (OAuth) login only**. There is no API-key or
   `--bare` mode.
3. Isolation comes from layered controls, and the factory **verifies** them rather than trusting the flags:
   - The launch dir is a private temporary directory.
   - Flags: `--setting-sources ""`, `disableAllHooks`, `--strict-mcp-config`, `--disable-slash-commands` and `dontAsk`.
   - Every read and write allow rule is path-scoped to the worktree, the private inputs and the profile snapshot;
     the run's own projects entry (tool-result spills) is additionally readable, never writable.
   - The child's environment is a strict allowlist.
   - The phase fails closed unless Claude's own `system/init` event confirms the expected tools, MCP servers,
     skills, plugins, permission mode and cwd.
   - After the run, a launch-dir audit and a global-residue check fail closed on any unexpected write.

## Rationale

- **CLI over SDK:** the SDK would place a TypeScript or Python runtime inside the Go control plane, which breaks the
  Go-only backend convention and adds a second process tree to supervise. The path rules, the init-event check
  and the post-run audits give about the same guarantee as `canUseTool`, and each can be checked separately.
- **CLI over the direct API:** the API needs per-use API-key billing, which was rejected in favour of the
  subscription, and we would have to rebuild file tools that are security-critical.
- **Subscription only:** this is the account in use. Probes showed that subscription mode, with an empty settings
  source list and an allowlisted environment, does not load the global `CLAUDE.md`, user plugins, MCP servers, skills, or
  `CLAUDE.md`/`AGENTS.md` files planted in the worktree. The remaining gap compared with `--bare` is that Claude's
  built-in plugins still load, which the init-event check pins to a known list.
- **Verify, don't trust:** Claude Code updates itself. A flag whose meaning changes without an error would quietly
  remove isolation, so the runtime check and the failure classification replace any minimum-version pin.

## Consequences

- A new Claude built-in plugin, or a change to the init-event shape, makes phases fail closed until the
  allowlist or parser is updated on purpose.
- Claude spills large tool results to `<projects>/<entry>/<session>/tool-results/` even without session
  persistence, and a private `CLAUDE_CONFIG_DIR` would lose the subscription login. So the adapter pre-creates the
  run's own projects entry, grants the model read-only access to it, and after the run removes only those spill
  files (recording name and size, never content) and empty directories. Any other write there still fails closed
  and is kept (`acf-62`).
- Usage-limit failures are classified but never retried by the adapter.
- `Read` stays limited to the phase's paths, which is stronger than Copilot's `view`. Copilot's own environment
  hardening is tracked separately (`acf-35`).
- If a future CLI stops exposing init-event details or path-scoped rules, the Agent SDK becomes the fallback;
  that switch requires a new ADR.
- Codex and OpenCode each become one driver. Codex needs its own decision on whether its always-present shell
  inside an OS sandbox is acceptable.
