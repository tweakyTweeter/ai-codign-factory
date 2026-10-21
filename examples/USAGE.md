# Foreground factory

Build with `go build -o /tmp/factory ./cmd/factory`. Darwin and Linux are
supported. Run `/tmp/factory help` or `<command> --help` for flags.

```text
factory init --repo /path/to/repo
factory doctor --repo /path/to/repo --config /path/to/config.json
factory run --repo /path/to/repo --config /path/to/config.json --prompt-file /path/to/task.txt
factory run --repo /path/to/repo --config /path/to/config.json --prompt-file - --task project-123 --profile custom
factory status --repo /path/to/repo
factory status --repo /path/to/repo --run RUN_ID
factory cancel --repo /path/to/repo --run RUN_ID
factory reconcile --repo /path/to/repo
factory release --repo /path/to/repo --run RUN_ID --reason "Reviewed and retained for manual integration"
factory review --repo /path/to/repo --run RUN_ID
factory review-publish --repo /path/to/repo --run RUN_ID
factory review-sync --repo /path/to/repo --run RUN_ID
factory integrate --repo /path/to/target-checkout --run RUN_ID
factory decisions await --repo /path/to/repo --ids DECISION_ID[,DECISION_ID...] [--poll 15s] [--timeout 0]
```

`decisions await` is read-only: it waits until each listed human decision bead
is answered in Scotty Needs You and prints the comments. Used by intake; see
[intake workflow](../docs/intake-workflow.md).

Flags follow the command. JSON results are written to stdout (including a run
snapshot on run failure), diagnostics to stderr. Exit codes: 0 success, 1
runtime failure, 2 usage error, 130 cancellation. `cancel` acknowledges a durable
request, not completed teardown. The foreground controller polls that request.
Ctrl+C / SIGTERM also cancels the current command.

`init` creates a private default config at COMMON_GIT_DIR/factory/config.json
without overwriting it. Replace the worker placeholder before running.
`examples/factory.json` is a template, not an installed integration.
The config has strict version-1 JSON decoding; unknown fields are errors.
Worker and every check require literal `argv` and a positive `timeout` no
greater than 24h. At least one check is required. Optional `profiles` map names
to alternate worker commands; `--profile` selects exactly one. Optional
`beads_argv` defaults to `["bd"]` and may name an explicit local wrapper.

## Worker contract and trust

The worker's working directory is an isolated Git worktree at the caller's
explicit HEAD commit, on branch `factory/RUN_ID`. It receives these variables:

* `FACTORY_RUN_ID`: durable identifier
* `FACTORY_WORKTREE`: absolute working directory
* `FACTORY_REPO`: caller's resolved repository root
* `FACTORY_TASK_ID`: optional Beads ID, empty without `--task`
* `FACTORY_PROMPT_FILE`: private UTF-8/text prompt copy (up to 1 MiB)
* `FACTORY_CONTEXT_FILE`: private JSON with run, repository and prompt/task paths

Checks receive the same variables. Commands read prompt/context files using
these environment variables; a wrapper may translate them to its external
CLI's supported arguments. Factory performs **no shell interpolation**, no
automatic positional prompt injection, and no implicit tool approvals.
An explicitly configured shell command is allowed but is the user's choice.
Use absolute worker/wrapper paths; relative executables resolve relative to the
worktree (PATH lookup otherwise follows Go exec semantics).

This is **trusted local execution, not a sandbox**. Commands inherit the
caller's environment (except stale `FACTORY_*` values), filesystem, credentials
and network access. Do not run untrusted code or place secrets in argv/config,
prompt or logs. Factory never adds `--allow-all-tools`, enables deployment, or
claims workers cannot touch the main checkout. Process-group teardown uses
SIGKILL before reaping the owned leader to avoid PID reuse. Deliberately
daemonizing/setsid processes can escape supervision: inspect them manually.
Log storage is capped at 1 MiB per stage (excess drained/discarded), and at most
32 checks are accepted. Context/config/prompt/task evidence is private under the
common Git directory (0700 directories, 0600 files), not copied into worktrees.

Only committed content is present in a new worktree. Dirty/untracked source,
agent profiles and user configs are **never copied**. Provide explicit wrapper
paths or committed non-secret context; do not assume untracked agents exist.
The optional supervised adapter explicitly snapshots selected profiles; it
does not rely on automatic worktree discovery. There is no installed Conductor
dependency or claimed Conductor capability probe.
`doctor` only validates config and executable availability. A configured
Conductor wrapper is generic external command execution, not a native adapter.

## Safety and recovery

One run is admitted per common Git directory, including linked worktrees.
Flock covers the entire foreground run; SQLite persists admission and
transitions before external side effects. PID and random launch token are
evidence, **never** authority for signalling after restart. Success ends
`awaiting_review`; all outcomes retain their branch/worktree and block new runs
until explicit `release`. `release` means acknowledgment, NOT integration,
landing, task completion or deletion. It never closes/unclaims a Beads issue.

After a controller crash, `reconcile` acquires the lock, marks unfinished runs
`needs_attention`, and does not relaunch or kill any PID. Orphans and uncertain
claims require manual inspection. Only after stopping surviving commands and
checking ownership may an operator use:

```text
factory release --repo /path --run RUN_ID --reason "Manually stopped orphans and verified claim ownership" --ack-orphan
```

There is no automated retry, worktree cleanup, commit, push, merge, remote sync
or unclaim. Task closure and release can be automated only by explicit
`integrate` after exact candidate approval and successful target checks; see
[human approval and integration](../docs/human-approval.md). No daemon is left
running after a normal CLI exit.
`--task` requires `bd show` to report a single open, unassigned task and
`bd ready --json --limit 0` to include it. The controller journals claim intent
then executes `bd update ID --claim --actor factory-RUN_ID --json` before
worktree/worker launch and verifies ownership with show and heartbeat.
Claim command failure is ambiguous and fails closed with `needs_attention`.
Factory does not change `BD_*` globals or infer ownership after failure.
Each Beads invocation adds `--sandbox` to suppress configured Dolt auto-push
for that command only. Custom Beads wrappers must support that flag.

Claimed runs renew node-local Beads leases every 30 seconds and check explicit
assignee/status and the heartbeat response before stages and the final gate.
Ownership loss, heartbeat failure or cancellation stops the owned process and
marks the claimed run `needs_attention`; the issue is never unclaimed/closed.
Renewal ends when the foreground command exits, including `awaiting_review`.
The local lock/lease does not protect against another machine's claims.
Recheck ownership and candidate contents before manual integration.

## Supervised configuration and adapters

`supervised.json` and the four `*-policy.json` files are **templates**. Substitute
absolute binary, policy and source profile paths before use. Do not add a worker
or legacy worker `profiles` to a workflow config. The enforced order is Producer,
optional selected specialists, one Dev, configured checks, independent QA, then
review. Phase names must be unique letters/digits/`-`/`_` (1–64 characters).
Every phase has `{name,role,command:{argv,timeout}}`. A specialist additionally
requires `profile`; it runs only when that name is selected with `--profile`.
No specialist runs by default. Conductor may be an explicitly configured
task-internal phase command, not a second factory scheduler.

The phase environment adds:

* `FACTORY_PHASE`, `FACTORY_ROLE`, `FACTORY_PROFILE`
* `FACTORY_OUTPUT_DIR`: private phase evidence directory outside the worktree
* `FACTORY_RESULT_FILE`: `OUTPUT_DIR/result.json`
* `FACTORY_PHASE_CONTEXT_FILE`: live JSON manifest with task/prompt paths,
  prior phase artifact directories, current run/check evidence and fingerprint
* `FACTORY_PATCH_FINGERPRINT`: the pre-phase candidate hash

Dev, QA and review commands must write a strict v1 result:

```json
{"version":1,"verdict":"pass","summary":"Acceptance evidence assessed","fingerprint":"64-character candidate hash","findings":[]}
```

Allowed verdicts are `pass`, `fail`, `inconclusive`. Findings require
`{severity:"info|warning|blocking",summary:"nonempty"}`. Only passing, valid
evidence without blocking findings reaches the gate. Dev evidence binds its
**post-change** candidate; QA and review bind their unchanged pre-phase
candidate. The fingerprint includes HEAD, staged changes, file modes and all
worktree files (including ignored/untracked files). Trees larger than 128 MiB,
100,000 files or an index diff above 1 MiB fail closed; these bounds are not
configurable safety bypasses. Producer/planners/QA/review may not modify the
worktree, index or HEAD. Mutation guards detect net changes; they do not prevent
access or constitute an OS sandbox. Checks can produce build artifacts before
QA; changes after QA invalidate evidence. Evidence is only valid at the gate:
humans must revalidate after any subsequent edit.

`factory copilot --policy PATH` is a controller phase adapter for the standalone
Copilot 1.0.100 contract. The explicit source agent file is copied byte-for-byte
into a private snapshot `.github/agents/AGENT.agent.md`, selected with `--agent`
and `--add-dir`, and its SHA-256 recorded in `adapter.json`. Original profile
content is retained; the explicit runtime tool policy overrides its tool
declarations. The adapter records argv, prompt, JSONL stdout, stderr and final
response. It requires one final assistant answer and a successful terminal
result, not merely process exit zero. Dev must report the final JSON outcome
`{version:1,status:"complete|blocked|inconclusive",summary:"..."}`; the adapter
normalizes completed implementation to a post-change v1 verdict. QA/review must
return the v1 verdict JSON directly. Missing capabilities must be reported,
not assumed; blocked Dev does not pass.

The tool allowlist is explicit. Read-only roles support view/rg/glob, and Dev
may additionally expose create/edit/apply_patch **only with explicit write
approval**. Tool names vary by model, hence all three write aliases in the Dev
template. These are user-approved write permissions, not implicit approvals.
Shell, task/delegation and MCP tools are unavailable. No blanket approvals are
added; `COPILOT_ALLOW_ALL` is overridden to `false`. No-ask-user, disabled
built-in MCPs, no-auto-update and no-custom-instructions flags are passed.
Launch boundary: Copilot's cwd is a fresh private temp directory (0700,
rejected if inside any git checkout, removed after the session) containing
`.github/copilot/settings.local.json` = `{"disableAllHooks":true}`, a profile
copy and private copies of sealed phase inputs and completed evidence.
User/repo hooks are therefore intentionally bypassed for this product-owned
session so evidence stays deterministic (a global agentStop hook otherwise
appended a second final answer; multiple final answers still fail closed).
The candidate is reached via `--add-dir WORKTREE` and the prompt requires
absolute repository paths; launch configuration is never written to the
candidate or global config, and the normal user home is kept so authentication works
(credentials are never copied). `adapter.json` records the launch dir,
settings and worktree. The adapter requires controller-sealed phase context.
It grants no live run-output directory access and passes `--disallow-temp-dir`.
Input JSON references are rebased into the private copies; candidate paths
remain unchanged. Copied inputs/profiles/settings and retained adapter
provenance are checked for mutation before accepting a response.

The controller persists seals for inputs and completed output trees/logs,
rechecking before and after commands and at review/sync/integration boundaries.
Changed evidence fails closed without refreshing the old seal. Legacy unsealed
runs require a new verified run. Exact-path `write(path)` rules do not provide
a documented recursive directory write grant, so Dev write approval remains
non-path-scoped. Global settings/extensions and equivalent host privileges
can exceed adapter grants: filesystem/network/credential isolation is **not**
guaranteed. Default QA independently assesses task acceptance,
candidate and check evidence; it does not independently execute tests.

`factory roborev-import --report PATH --fingerprint REVIEWED_HASH` is an
alternative review phase adapter. It accepts canonical roborev v0.71.0 schema
v2 JSON (`schema_version`, `verdict`, `summary`, `findings`). Each finding
requires severity, problem, fix and location (string or null). Every roborev
finding is blocking; unable_to_review is inconclusive. The explicit attested
hash must match the controller candidate, and the source report is retained.
Report freshness/origin remains the configured trusted caller's responsibility;
an unrelated old report is not independent evidence. Native roborev launch is
deliberately unsupported because its upstream Copilot runner adds
`--allow-all-tools`. Native exit success is never treated as a clean verdict.
