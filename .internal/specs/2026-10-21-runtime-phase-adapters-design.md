# Runtime phase adapters: shared runner + Claude driver

- **Date:** 2026-10-21
- **Beads:** feature `acf-23`; brainstorming session `acf-28`
- **Status:** spec approved; stress-tested (`acf-34`, results at the end)

## Problem

Pipeline phases (Producer, Dev, QA, Review) can only run through
`factory copilot` (`internal/factory/adapters.go`). With no Copilot credits the
factory cannot run any ticket. The phase prompts also point at upstream
awesome-copilot profiles: `ai-team-producer` has been removed from APM, and
`independent-review` never existed.

## Decisions taken in brainstorming

| Question | Decision |
| --- | --- |
| Scope of the first slice | Shared runtime layer + **Claude** driver. Codex and OpenCode are follow-up tickets that each add one driver. |
| Phase role profiles | Neutral roles in `.apm/agents/`: new `factory-dev`, `factory-qa`, `factory-reviewer`; the Producer phase reuses `factory-producer`. |
| Claude authentication | **Subscription (OAuth) only.** No `--bare`/API-key mode. |
| Structure | **Approach A**: extract a shared runner from `Copilot()`; Copilot and Claude become drivers. Rejected: a copied `factory claude` adapter (forks security logic), and Conductor (no sealing or hook control). |

## Architecture

### Shared runner: `internal/factory/runtime.go`

`RunPhase(ctx, policyPath string, d Driver) error` holds everything that is
runtime-independent today, moved out of `Copilot()` without behaviour change:

1. Validate the controller environment (`FACTORY_ROLE`, `FACTORY_OUTPUT_DIR`,
   `FACTORY_RESULT_FILE == OUTPUT_DIR/result.json`, `FACTORY_WORKTREE`,
   `FACTORY_CONTEXT_FILE` / `FACTORY_PHASE_CONTEXT_FILE`; all absolute).
2. Read the policy with `readBounded` and decode it strictly (v1 or v2, see Policy).
3. `privateLaunchDir()`: fresh 0700 temp directory; fail if any ancestor holds `.git`.
4. `d.LaunchSettings(launch)`: the driver writes its hook-disabling settings
   under a driver-chosen relative directory, which is sealed.
5. Copy the resolved profile byte for byte into
   `OUTPUT/profile-snapshot` and `launch/profile-snapshot`, and record its SHA-256.
6. `copilotInputs` (renamed `privateInputs`): copy sealed inputs and completed
   evidence into `launch/inputs`, and seal them.
7. Build the role instructions (the current text, unchanged) plus the
   per-role output contract.
8. Write and seal `adapter.json`, `adapter-prompt.txt`, `adapter-argv.json`.
9. Run `d.Argv(...)` from `launch` with `d.Env(...)`, through `executeStreams`, with
   stdout and stderr capped at `outputLimit`.
10. Verify the artifact integrity, the input, profile and settings seals, and that output was not truncated;
    then call `d.PostRun(launch, ev)`, and fail closed if it returns an error.
11. Write `<runtime>.jsonl` and `<runtime>.stderr.log` (Copilot keeps
    `copilot.jsonl` and `copilot.stderr.log`).
12. `d.Final(raw, in)`, then `response.txt`, then apply the role contract:
    - QA/Review: write `result.json`; `readVerdict` must match `FACTORY_PATCH_FINGERPRINT`.
    - Dev: strict `{version:1,status,summary}`; only `complete` passes; write a
      `pass` verdict over the current `Fingerprint(worktree)`.
    - Producer/specialist: no result file (as today).

### Driver interface

```go
type Driver interface {
    Name() string                                          // "copilot" | "claude"
    Tools(role string, grants []string) (DriverTools, error) // neutral grants -> runtime tools/rules
    LaunchSettings(launch string) (relDir string, err error) // write + return the dir to seal
    Argv(p Policy, in LaunchInputs) []string
    Env(base []string) []string                             // final child environment
    Final(raw []byte, in LaunchInputs) (string, error)      // stream -> final answer; verifies reported isolation
    Notice() string                                         // isolation limits for adapter.json
    PostRun(launch string, ev *PhaseEvidence) error         // driver-specific audits/cleanup; Copilot: no-op
}
```

`LaunchInputs` carries `launch`, `inputs`, `profileSnapshotPath`,
`worktree`, `instructions` and the mapped `DriverTools`.

### Copilot driver

It contains the current argv, the settings (`.github/copilot/settings.local.json` =
`{"disableAllHooks":true}`), env (`COPILOT_ALLOW_ALL=false`), `copilotFinal`
and the notice. No behaviour change. v1 tool names (`view`, `rg`, `glob`, `create`,
`edit`, `apply_patch`) and approvals (`read`, `write`) are kept for v1 policies.
For v2 grants, `read` maps to `view, rg, glob`, and `write` (Dev only) adds
`create, edit, apply_patch` with `--allow-tool write`.

### Claude driver

Argv, run from `launch`:

```text
<executable> -p <instructions>
  --append-system-prompt-file <launch>/profile-snapshot/<agent>.md   # frontmatter stripped
  --tools <T> --allowedTools <A>
  --permission-mode dontAsk
  --setting-sources ""
  --settings <launch>/.claude-launch/settings.json            # {"disableAllHooks":true}
  --strict-mcp-config
  --disable-slash-commands
  --no-session-persistence
  --output-format stream-json --verbose
  [--model <model>]                                           # only when the policy sets it
  --add-dir <worktree> --add-dir <launch>/inputs
```

Tool mapping:

| Grant | `--tools` | `--allowedTools` |
| --- | --- | --- |
| `read` | `Read,Grep,Glob` | `Read(//R/**)`, `Grep(//R/**)`, `Glob(//R/**)` for each R in {worktree, launch/inputs, launch/profile-snapshot} |
| `write` (Dev only) | adds `Edit,Write` | adds `Edit(//<worktree>/**)`, `Write(//<worktree>/**)` |

`Bash`, `WebFetch`, `WebSearch`, `Agent`, `NotebookEdit` and any MCP tool are
never granted. Every allow rule is path-scoped. With `dontAsk`, anything outside those paths is
denied without a prompt: verified by probe for Read, Grep and Write outside the scope, and for writes into `inputs/`.
Denials are recorded as evidence and do not by themselves fail the phase.

The profile body is appended with `--append-system-prompt-file`, which keeps Claude Code's
default system prompt (tool-use guidance and environment) and adds the role on top.
Replacing that prompt would make tool use worse. Permission rules use the `//` prefix for
absolute paths, because a single `/` is read relative to the settings file. The frontmatter is
removed when the launch copy is written: the copy under `OUTPUT/profile-snapshot`
stays byte-identical to the source and is what gets hashed, and the stripped launch
copy is sealed as well.

`PostRun` (Claude):
- **Launch-dir audit.** Before the run, record the launch dir's file manifest (inputs, profile
  snapshot, settings). After the run, any new, removed or changed file fails the phase closed.
  A probe showed Claude leaves the launch dir empty, so a normal run cannot trigger a false alarm.
- **Global residue cleanup.** Claude creates `~/.claude/projects/<sanitised launch path>/memory/`
  for each launch dir, which breaks the "nothing global is written" property. After the run, find the
  `~/.claude/projects` entry whose name contains the launch dir's unique basename. If it is only empty
  directories, remove it. If it contains any file, keep it, record its path in `adapter.json`, and fail
  the phase. Only that uniquely named entry is ever touched, so interactive sessions are unaffected.

`Env`: a strict **allowlist**, not inheritance. The child gets only `PATH`, `HOME`, `USER`, `LOGNAME`,
`SHELL`, `TMPDIR`, `LANG`, `LC_*`, `TERM`, plus `CLAUDE_CONFIG_DIR` if it is set. Everything else is dropped,
including every `ANTHROPIC_*` (no API-key billing switch), `CLAUDE_CODE_*` and `CLAUDECODE` (when the
factory is launched from an agent session these include the parent session's messaging socket and token),
cloud-provider credentials (Google/Vertex, AWS/Bedrock), proxies, `NODE_OPTIONS` and `FACTORY_*`.
`adapter.json` records the dropped variable **names**, never their values. A probe confirmed that
subscription OAuth works with this environment. A policy-level `env_allow` (for a corporate proxy, say)
is deliberately out of scope until it is needed. The allowlist also drops
`CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD`, so a `CLAUDE.md` or `AGENTS.md` planted in the worktree is not
loaded automatically as instructions (verified by probe). A phase may still `Read` such a file, but then it is
ordinary repository content, which the role prompt treats as untrusted data.

`Final(raw, in)`: JSONL; every non-empty line must decode and have a `type`.
The **first** event must be `system`/`init`, and it must confirm the isolation actually in effect;
otherwise the phase fails closed. This catches CLI changes in meaning that raise no error:
- `tools` is exactly the mapped tool set;
- `mcp_servers` and `skills` are empty;
- `permissionMode == "dontAsk"`;
- every `plugins[].source` is in the built-in allowlist
  (`cc-plugin-agents-md@builtin`, `cc-plugin-telemetry@builtin`, `cc-plugin-plugin-authoring@builtin`);
- `cwd` is the launch dir.

The init event's `model` and `claude_code_version` (when present) are recorded. Separately, before the phase
the driver runs `<executable> --version` (with the same allowlisted env and launch cwd) and records the
output in `adapter.json`; `factory doctor` shows it. There is deliberately no minimum-version pin.
Exactly one `{"type":"result"}` event, with `subtype == "success"`,
`is_error == false` and a non-empty `result`, must be the last event. `result` is the
final answer. `permission_denials` from the result event are copied into
`adapter.json` evidence.

`Notice`: "private launch dir outside any checkout; no user/project/local settings sources (no user hooks, plugins, permissions or global CLAUDE.md); disableAllHooks; no MCP; no skills; exact tool list with dontAsk; reads limited to the worktree, private inputs and profile; Dev writes limited to the worktree; env is an exact allowlist; built-in plugins (agents-md, telemetry, plugin-authoring) still load; auto-memory points at an empty per-launch-dir folder that is removed after the run; isolation verified from the init event; not a sandbox".

Path rule: before any allow rule is built, `Tools` rejects (fails closed, failure_reason `policy`)
any worktree, inputs or profile path, in its given or symlink-resolved form, that contains one of
`* ? [ ] { } ( ) , \`, any whitespace, any control character or invalid UTF-8. Such characters would
widen, redirect or break a permission rule (`\` is the gitignore escape). Each form must also be
absolute and canonical (`filepath.Clean(form) == form`: no `.` or `..` component, no `//`, no trailing
`/`) and must not be `/`. Paths are never escaped or quoted.

Verified by probe against Claude Code 2.1.306: with these flags, the `init` event
reports only the granted tools, no MCP servers, no skills and only the built-in plugins,
and the global `~/.claude/CLAUDE.md` is not in context.

### Policy v2

```json
{
  "version": 2,
  "runtime": "claude",
  "executable": "/absolute/path/to/claude",
  "role": "dev",
  "agent": "factory-dev",
  "source_profile": "/absolute/path/to/.apm/agents/factory-dev.agent.md",
  "grants": ["read", "write"],
  "model": "opus"
}
```

Validation (strict decode; unknown fields rejected):

- `runtime` must be one of `copilot` or `claude`.
- `role` must match `FACTORY_ROLE` and be one of `producer`, `specialist`, `dev`, `qa` or `review`.
- `agent` must satisfy `safeName`.
- `executable` and `source_profile` must be absolute paths.
- `grants` must be non-empty, contain no duplicates, and use only `read` and `write`.
- `grants` must include `read`.
- `write` is allowed only when the role is `dev`.
- `model` is optional and must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`. Claude passes it as `--model`; the Copilot
  driver rejects a v2 policy that sets it instead of silently ignoring it. When it is omitted, Claude uses the account
  default. Either way, the model actually used, read from the stream's `system/init` event, is recorded in
  `adapter.json`. The example QA and Review policies pin a model.

v1 policies keep their current schema and validation and always mean `runtime: copilot`.

### CLI

- `factory agent --policy P` accepts v1 and v2 policies.
- `factory copilot --policy P` stays. It accepts v1, or v2 with `runtime: copilot`, and rejects any other runtime.
- The `doctor` and help text list `agent`.

### Profiles

The new files in `.apm/agents/` use frontmatter that every runtime accepts:
`name`, `description` and `mode: subagent`. `apm install` deploys them to all targets.

- `factory-dev`: implements the task only inside the worktree, using absolute paths. It reports
  `blocked` for unsupported capabilities and returns the Dev JSON contract.
- `factory-qa`: assesses the actual patch independently and returns the verdict JSON.
- `factory-reviewer`: gives an independent code review and returns the verdict JSON.
- `factory-producer`: gains a section headed "When run as a pipeline phase", which
  says to return a plan summary and its limitations and not to wait for questions.

`examples/` gains v2 Claude policies (`claude-*-policy.json`) and a
`supervised-claude.json`. The Copilot examples move to v2 `runtime: copilot` and
the `factory-*` profiles, which removes the dangling `ai-team-producer` and
`independent-review` references.

## Error handling

The runner fails closed and does not retry when any of the following occurs:

- a policy error, or a runtime that conflicts with the subcommand;
- a launch dir inside a checkout;
- inputs, profile or settings mutated during the run;
- truncated output;
- a stream error from `Final`;
- a contract violation: Dev not `complete`, a verdict that does not parse, or a fingerprint mismatch.

Every failure is classified as `failure_reason` in `adapter.json` and in the phase error, so that
`factory status` says why it failed:
- `usage_limit`: a rate-limit event, or a result reporting that the subscription limit was reached;
- `timeout`: the controller deadline;
- `isolation_mismatch`: init-event checks, launch-dir audit, residue;
- `stream_invalid`;
- `contract`;
- `policy`.

No retry is done in the adapter. Partial Dev edits need nothing new: a failed run's branch and worktree are retained
for inspection, never sealed as a candidate and never integrated (`controller.go`), and the operator uses
the existing `status` / `release` / `reconcile` commands.

`adapter.json` records `runtime`, the policy, the profile hash, the
launch dir, the settings content, the mapped tools and rules, the notice and the permission denials.

## Testing

0. **First task, before any code moves:** characterization (golden) tests against today's Copilot
   code. For each role (producer, dev, qa, review) they capture:
   - the exact `adapter-argv.json`, with temp paths normalised;
   - the exact `adapter-prompt.txt`;
   - the `adapter.json` keys and notice;
   - the child's `COPILOT_*` environment.

   The refactor must keep every golden file byte-identical. (Today's tests only substring-match a few
   flags, so dropping `--no-custom-instructions` or `--disable-builtin-mcps` would go unnoticed.) A golden
   file changes only through a deliberate, separately reviewed update.
1. Existing Copilot and roborev tests pass without being edited, and v1 policies still decode.
2. Policy v2 table tests cover each validation rule above.
3. Claude argv tests, one per role, check the exact `--tools` and `--allowedTools`, Dev
   writes limited to the worktree, forbidden tools absent, and the flags for isolation, settings and appended system prompt (including the `//` prefix on the write rules).
4. `claudeFinal` table tests cover the init checks (an extra tool, an MCP server, a skill, a non-built-in plugin,
   the wrong permission mode, the wrong cwd, a missing or late init event, each failing) and the result checks: success; no result; two results; data after
   the result; `is_error`; an error subtype; an empty result; malformed JSON; a missing type.
5. A fake `claude` executable runs end to end through `RunPhase` and checks:
   - Dev `complete` passes, and Dev `blocked` with exit code 0 fails;
   - a QA verdict with the wrong fingerprint fails;
   - mutation of inputs or settings fails closed;
   - the child environment is exactly the allowlist (a fake prints its env; `ANTHROPIC_*`,
     `CLAUDE_CODE_*` and credential variables set in the parent are absent), and the dropped names are recorded.
6. A live smoke test runs only when `FACTORY_LIVE_CLAUDE=1` (skipped otherwise): one read-only phase
   with a fixed answer, plus a Dev phase asserting that reads and writes outside the worktree are denied and that
   no `~/.claude/projects` entry for the launch dir remains, plus a phase with marker `CLAUDE.md` and `AGENTS.md`
   files planted in the worktree asserting that the marker is not in context.
7. `PostRun` tests: a fake that drops a file in the launch dir fails; a fake that leaves an empty
   residue tree is cleaned; a fake that leaves a non-empty residue fails and keeps it; unrelated
   `~/.claude/projects` entries are never touched (HOME points at a temp dir in tests).

Gates: `go vet ./...`, `gofmt -l .`, `git diff --check`,
`GORACE=atexit_sleep_ms=0 go test -race -timeout 15m ./...`.

## Out of scope

- Codex and OpenCode drivers, which are follow-up tickets. Codex needs a decision on
  whether its always-present shell inside an OS sandbox is acceptable.
- API-key or `--bare` Claude mode.
- OS-level sandboxing of any runtime.
- Any change to controller admission, approval or integration.

## Docs to update

`examples/USAGE.md` (adapter section), `docs/architecture.md` (the runtime adapter
row and "Copilot workers"), `docs/supervised-workflow.md` (examples), and
`HANDOFF.md`.

## Stress Test Results: runtime phase adapters

Session `acf-34`, 2026-10-21. Live probes were run against Claude Code 2.1.306.

### Resolved Decisions
- **Read scope:** Read, Grep and Glob are limited, for every role, to the worktree, the private inputs and the profile snapshot.
  A probe showed reads outside that scope are denied under `dontAsk`. Denials are recorded as evidence and do not fail the phase on their own.
- **Write scope:** Dev writes are limited to the worktree (a probe showed writes outside it and into `inputs/` are denied), plus an
  independent launch-dir audit after the run.
- **Global residue:** each Claude run creates `~/.claude/projects/<launch>/memory/`. It is removed when it is only empty
  directories; any file in it fails the phase.
- **Environment:** a strict allowlist replaces "strip ANTHROPIC_API_KEY". Without it, the parent agent session's
  messaging socket and token, session IDs and cloud credentials would be inherited. Subscription OAuth works with
  the allowlisted environment (probe).
- **Model:** an optional, validated `model` in policy v2. The model actually used is always recorded, and Copilot rejects the field
  rather than ignoring it.
- **CLI drift:** the phase fails closed unless the `system/init` event confirms the isolation: exact tools, no MCP or skills,
  `dontAsk`, only built-in plugins, and the launch cwd. `--version` is recorded; there is no minimum-version pin.
- **Usage limits:** fail closed with no retry; the reason is classified as `failure_reason`. A failed run's worktree
  is retained and never integrated (existing controller behaviour).
- **Refactor safety:** Copilot golden tests (argv, prompt, adapter.json, env, for each role) are the first task and must stay
  byte-identical through the refactor.
- **Mechanism:** keep the `claude` CLI. The Agent SDK would put TypeScript or Python in the Go control plane, and the
  direct API would need API-key billing and our own file tools.
- **Planted instruction files** (added during self-review): a worktree `CLAUDE.md` or `AGENTS.md` is not loaded automatically under the
  allowlist (probe); a live smoke assertion locks this in.

### Changes Made
- Path-scoped read rules; added `PostRun` (launch-dir audit and residue cleanup) to the Driver interface.
- Env allowlist; optional `model`; `Final(raw, in)` with init-event verification; `failure_reason`.
- Testing: golden tests first (item 0), `PostRun` tests (item 7), and an expanded live smoke test.

### Deferred / Parking Lot
- `acf-35`: apply the same env allowlist to the Copilot adapter. It is kept out of this refactor so
  Copilot behaviour does not change.
- Policy-level `env_allow` (for example, a corporate proxy): only when needed.
- Agent SDK fallback if a future CLI stops exposing init-event details or path-scoped rules.
- Codex and OpenCode drivers: follow-up tickets. Codex needs a decision on whether its always-present shell inside an OS sandbox is acceptable.

### Confidence Assessment
- Overall: **High**. Every isolation claim was checked by a live probe and is also guarded by a runtime check
  (init event, audits) or a test.
- Areas of concern: the event shapes used to detect usage limits are not a documented contract. When the reason
  is unrecognised, the failure is classified `stream_invalid` (still fail-closed). The built-in plugin allowlist will need
  a deliberate update whenever Claude ships a new built-in plugin; until then, such a run fails closed.

