# AI Coding Factory — Agent Handoff

_Last updated: 2026-10-21 (runtime phase adapters added; Claude slice complete). Read this first, then `AGENTS.md`, `README.md`, and `docs/`. Any supported agent (Claude Code, Codex, OpenCode, Copilot) can continue from here._

## 1. What this is

This is a **local-first, human-supervised AI coding factory** written in Go.

- It takes one **Beads** (`bd`) task.
- It runs that task in an **isolated Git worktree**, in sequence:
  1. Producer
  2. Dev
  3. deterministic checks
  4. independent QA
  5. review, using specialist agent phases (run through the Copilot or Claude Code adapter, chosen per phase by a policy file).
- It records everything in a **SQLite journal**.
- It publishes the candidate for **human approval in Scotty**.
- It integrates locally only after explicit approval, using a conservative merge.

The latest addition is a **skill-driven intake stage**. Before the pipeline runs, it turns a vague request into an approved spec and a set of Beads tickets.

Conventions (must keep):
- Use Go for backend code and Beads for all task tracking (`bd ready`, `bd update --claim`, `bd close`).
- Do not commit, push or run `bd dolt push` unless the user explicitly asks.
- Use a commit trailer naming the agent that did the work (see `AGENTS.md` → Agent portability).
- Never overwrite APM-managed `.github/agents` / `apm_modules`, or existing skills. Augment them instead.
- Keep it agent-agnostic: shared skills in `.agents/skills/`, shared subagent roles in `.apm/agents/` (deployed by `apm install` to `.github/agents/`, `.claude/agents/`, `.opencode/agents/`), skill links in `.claude/skills/`; per-agent local state is gitignored.
- Product code always calls `bd` with `--sandbox`.

## 2. Architecture

```
                         ┌───────────────────────────────────────────┐
  User request ───────►  │  /factory-intake skill (any agent)        │
                         │  .agents/skills/factory-intake/SKILL.md   │
                         │                                           │
                         │  factory-producer ──draft scope/seams──┐  │
                         │        ▲  grilling (subagent answers)  │  │
                         │        └── domain-modeling,            │  │
                         │            codebase-design             ▼  │
                         │  factory-principal-engineer ─► to-spec    │
                         │                    │                      │
                         │        "Approve spec seams" decision ─────┼──┐
                         │                    ▼                      │  │
                         │                to-tickets                 │  │
                         │        "Approve ticket breakdown" ────────┼──┤
                         │                    ▼                      │  │
                         │  bd children: --parent EPIC               │  │
                         │   -l ready-for-agent --deps blocked-by:X  │  │
                         └────────────────────┬──────────────────────┘  │
                                              │ frontier loop           │
                                              │ (sequential)            │
                                              ▼                         ▼
 ┌──────────────────────── Go controller: cmd/factory ─────┐   ┌──────────────────┐
 │ internal/cli  ── run / status / cancel / reconcile ...  │   │ Scotty (Beads UI)│
 │                                                         │   │  "Needs You"     │
 │ internal/factory                                        │   │  human comments  │
 │  controller.go  ─ admission (1 run), lifecycle          │◄──┤  approve/reject  │
 │  workflow.go    ─ phase graph                           │   └────────▲─────────┘
 │  repo.go        ─ git worktree per run                  │            │
 │  process.go     ─ supervised child procs (darwin/linux) │            │
 │  runtime.go     ─ Driver interface, RunPhase (copilot,  │            │
 │                   claude), roborev import in adapters.go│            │
 │  store.go       ─ SQLite journal (runs, phases, events) │            │
 │  integrity.go   ─ evidence seals (candidate fingerprint)│            │
 │  human_review.go─ review-publish / review-sync ─────────┼────────────┘
 │  integration.go ─ integrate (approved + sealed only)    │
 │  decisions.go   ─ read-only decision waiter (bd poll)   │
 │  beads.go       ─ bd claim/notes/close (--sandbox)      │
 └───────┬─────────────────────────────────────────────────┘
         │ per run
         ▼
 ┌─────────── isolated worktree ───────────┐      ┌──────────────────┐
 │ Producer → Dev → checks → QA → Review   │ ───► │ candidate commit │
 │ (Copilot or Claude agents w/ policy JSON│      │ + sealed evidence│
 │  examples/*-policy.json, v2)            │      └────────┬─────────┘
 └─────────────────────────────────────────┘               │ human approved
                                                           ▼
                                            factory integrate → target checkout
```

## 3. What has been built

### Committed (`f7b32ad feat(factory): add supervised coding and approved local integration`)

| Feature | Where | Notes |
|---|---|---|
| CLI | `cmd/factory`, `internal/cli` | `init`, `doctor`, `run`, `status`, `cancel`, `reconcile`, `release`, `review`, `review-publish`, `review-sync`, `integrate`, `copilot`/`agent`/`roborev-import` adapters |
| SQLite run journal | `internal/factory/store.go` | Durable run, phase and event state; supports recovery and reconciliation |
| Single-run admission and lifecycle | `controller.go` | One active run at a time; cancel, release (`--ack-orphan`) and reconcile are covered |
| Git worktree isolation | `repo.go` | Each run gets its own worktree |
| Supervised processes | `process.go`, `wait_darwin.go`, `wait_linux.go` | Process-group control and orphan detection |
| Workflow engine | `workflow.go`, `examples/supervised.json` | Producer → Dev → checks → QA → review |
| Copilot phase adapter | `adapters.go`, `private_inputs.go`, `examples/*-policy.json` | Per-phase tool and permission policies |
| roborev report import | `adapters.go` | Imports canonical schema-v2 reports with an attested fingerprint |
| Evidence integrity seals | `integrity.go` | Binds the candidate fingerprint to its evidence |
| Scotty human approval bridge | `human_review.go`, `docs/human-approval.md` | `review-publish` creates a human gate bead; `review-sync` reads approved / rejected / not-approved |
| Approved local integration | `integration.go` | Integrates only approved, sealed candidates, conservatively |
| Beads integration | `beads.go` | Claim, notes and close, all with `--sandbox` |
| Docs | `README.md`, `docs/architecture.md`, `docs/supervised-workflow.md`, `docs/agent-setup.md`, `docs/external-runtimes.md`, `examples/USAGE.md` | |

### Committed (`979bc7c feat(factory): add skill-driven intake and human decision waiter`, Beads `acf-21`)

| Feature | Where |
|---|---|
| `factory decisions await --repo --ids A,B [--poll 15s] [--timeout 0]`: a read-only poll of `bd show`/`bd comments`. States are `answered`, `dismissed`, `cleared-without-answer` and `pending`. Output is JSON `{decisions, all_answered}`; exit code is 0 only when every decision is answered | `internal/factory/decisions.go`, `internal/cli/decisions.go`, `cli.go` |
| Tests that use a fake `bd`, including an assertion that the waiter makes no write calls | `internal/factory/decisions_test.go`, `cli_test.go` |
| `/factory-intake` skill: the full intake procedure | `.agents/skills/factory-intake/SKILL.md` |
| Tracker and domain contracts for the Matt Pocock skills (Beads-backed) | `docs/agents/issue-tracker.md`, `docs/agents/domain.md` |
| Intake documentation and the skill-mapping table | `docs/intake-workflow.md`, plus updates to `AGENTS.md`, `README.md`, `examples/USAGE.md` and `docs/supervised-workflow.md` |

### Committed: runtime-neutral phase adapters (branch `feat/runtime-phase-adapters`, Beads epic `acf-37` (feature bead `acf-23`), Claude slice complete; Task 11 bead `acf-47`)

| Change | Where |
|---|---|
| `Driver` interface and shared `RunPhase`; Copilot behaviour pinned byte-for-byte by golden files | `internal/factory/runtime.go`, `copilot_driver.go`, `testdata/copilot-golden/` |
| `factory agent --policy` (runtime-neutral); `factory copilot` refuses non-Copilot policies | `internal/cli/adapters.go` |
| Policy v2: grants `read`/`write` (write is Dev only), optional `model`; Copilot rejects `model` | `internal/factory/policy.go` |
| Claude driver: `claude -p` from a private launch dir, path-scoped tools, exact env allowlist, subscription auth only | `claude_driver.go` |
| Init-event isolation checks, `failure_reason` classification, never retries | `claude_stream.go`, `runtime.go` |
| Post-run launch-dir audit and `~/.claude/projects` residue handling (rmdir of empty trees only) | `claude_postrun.go` |
| New post-run evidence file `adapter-outcome.json` (Claude only) | `runtime.go` |
| Run worktrees now created at `<parent>/<repo>.factory-runs/<run-id>`, not under `.git`; the root must be a real 0700 directory owned by the user. Existing runs keep their stored paths. The factory never deletes worktrees | `repo.go`, `controller.go` |
| `factory doctor` reports `phase_runtimes`, parses `--policy` like the adapter, runs only `claude --version` | `internal/cli/cli.go`, `runtime.go` |
| Pipeline roles `factory-producer/dev/qa/reviewer`; examples `claude-*-policy.json`, v2 Copilot policies, `supervised-claude.json` | `.apm/agents/`, `examples/` |
| Opt-in live smoke test: `FACTORY_LIVE_CLAUDE=1 go test ./internal/factory -run ClaudeLive -count=1 -v` (subscription quota, real HOME) | `internal/factory/claude_live_test.go` |
| Docs and ADR | `examples/USAGE.md`, `docs/architecture.md`, `docs/adr/0001-claude-phase-worker-via-cli.md` |

Known limit: a repository whose path, or whose parent directory's path (or `TMPDIR`), contains whitespace or glob characters (`* ? [ ] { } ( ) ,`, backslash) cannot run Claude phases (`failure_reason=policy`).

`816ac62 chore: remove per-tool agent configs and add beads showcase` removed `CLAUDE.md` and the `.claude/`, `.codex/` and `.cursor/` hook files, and added `.beads/showcase/`.

### Committed: agent-agnostic intake (Beads `acf-22`, closed)

| Change | Where |
|---|---|
| Neutral subagent roles `factory-producer` and `factory-principal-engineer`, authored as APM local primitives and deployed to `.github/agents/` for Copilot | `.apm/agents/`, `.github/agents/factory-*.agent.md`, `apm.lock.yaml`; linked from `.claude/agents/` and `.opencode/agents/` |
| `factory-intake` and `beads` skills visible to Claude Code | links in `.claude/skills/` |
| Skill rewritten with a per-harness table (launch / continue subagent / load skill) and a fallback for user-only skills (`to-spec`, `to-tickets`, `wayfinder`, `handoff`: read `SKILL.md` directly) | `.agents/skills/factory-intake/SKILL.md` |
| `.claude/*` and `.opencode/*` ignored except the shared links | `.gitignore` |
| "Agent portability" rules | `AGENTS.md` |
| Agent-neutral wording | `docs/intake-workflow.md`, this file |

## 4. How to use it

```bash
go build -o "$TMPDIR/factory" ./cmd/factory     # building into /tmp has failed silently; use $TMPDIR
F="$TMPDIR/factory"; R=/path/to/repo; C=examples/supervised.json

$F init   --repo $R
$F doctor --repo $R --config $C
$F run    --repo $R --config $C --task <bead-id> --prompt-file task.txt
$F status --repo $R [--run RUN_ID]
$F review-publish --repo $R --run RUN_ID     # creates the Scotty gate
$F review-sync    --repo $R --run RUN_ID     # poll until human_review.decision == "approved"
$F integrate      --repo /path/to/target-checkout --run RUN_ID
$F decisions await --repo $R --ids <decision-bead-ids>
```

To run intake, use `/factory-intake <request>` from any supported agent at the repository root. Restart the agent after skills or agent roles are added.

The intake stages:
1. Creates an epic labelled `factory-intake`.
2. Grills a Producer subagent.
3. Has the Principal Engineer review the design.
4. Runs `to-spec` and asks the human to approve the seams.
5. Runs `to-tickets` and asks the human to approve the breakdown.
6. Loops over ready children one at a time: `run` → `review-publish` → `review-sync` → `integrate`.

It stops on any failure, rejection or ambiguity and never retries automatically.

How human decisions work:
- A decision is a child bead of type `decision`, labelled `human,factory-intake`.
- The human answers by commenting in Scotty "Needs You".
- Agents must **never** comment on decision beads, because any comment counts as the answer.

Useful Beads 1.3.1 facts:
- A Scotty answer adds a comment and removes the `human` label; the bead stays open.
- `bd show --json` omits comments, so use `bd comments ID --json`.
- `bd ready --parent EPIC -l ready-for-agent --unassigned --json --limit 0` returns only the ready children.

How the skills map:
- `grill-with-docs` (preferred) or `grill-me`, plus `domain-modeling` and `codebase-design`, are used during grilling.
- `to-spec` and `to-tickets` produce the spec and the tickets.
- `prototype` and `handoff` write only to `$TMPDIR`.
- `wayfinder` charts a `wayfinder:map` epic and then stops.
- `/implement` and `/implement-spec` are **deliberately excluded**. They auto-commit and use parallel branches, which conflicts with one-run admission and human-gated integration.

## 5. What remains to be built

### Immediate
1. All intake skills are installed in `~/.agents/skills` and linked into `~/.claude/skills`. If you run `/setup-matt-pocock-skills`, choose tracker **"Other"** so the Beads docs are kept.
2. **Dogfood intake end to end** on a small request, ideally from a non-Copilot agent. Check that:
   - the decision beads render in Scotty Needs You;
   - `decisions await` resumes after a comment;
   - the frontier loop completes one child.

   None of this has been tested yet.

### Open Beads milestones (`bd ready`)
| ID | Goal |
|---|---|
| `acf-13` | **Unattended supervision and serialized integration.** Run the factory as a durable service or daemon: a queue of ready beads, restart-safe, with integrations serialized. Humans still gate review acceptance and Git integration. |
| `acf-19` | **Live status and controls.** A Go-owned local API or dashboard that exposes phase, logs, cancellation and recovery next to Scotty. It must not take over Beads ownership or imply integration authority, and it must fail closed. |
| `acf-16` | **Restricted native roborev execution with provenance.** A foreground restricted launch that cannot widen permissions or mutate hooks or config. It captures the canonical report with origin and freshness provenance, and encodes the verdict in the result rather than in the process exit code. |
| `acf-23` | **Runtime adapters.** Claude slice is done (see section 3). Remaining: `codex exec` and `opencode run` drivers behind the same `Driver` interface (Claude slice delivered under epic `68m`; `6nq` stays open for the `codex exec` / `opencode run` drivers behind the same `Driver` interface; minor spec follow-up `acf-37.1` is open, and the ADR knowledge bead is `acf-36`). |
| `acf-35` | **Copilot env allowlist.** Deferred by design: the Copilot child still inherits the caller's environment, unlike Claude's exact allowlist. |
| `acf-12` | **Worker knowledge context and outcome capture.** Go adapters for approved source snapshots, obsidian-wiki, Memsearch and Graphify, with provenance and size bounds. No raw transcript capture. Deduplicate captures from interactive sessions and independent workers. |

Suggested order: dogfood intake (and a first real Claude pipeline run with `examples/supervised-claude.json`), then the Codex/OpenCode drivers and `kx1`, then `yth`, `vu6`, `qg6`, `0kk`.

## 6. Validation commands
```bash
go vet ./... && gofmt -l . && git diff --check
GORACE=atexit_sleep_ms=0 go test -race -timeout 15m ./...
```
All of these passed at handoff time.

## 7. Key docs
- `AGENTS.md`: agent rules and the `## Agent skills` block
- `docs/architecture.md`, `docs/supervised-workflow.md`, `docs/human-approval.md`
- `docs/intake-workflow.md`, `docs/agents/issue-tracker.md`, `docs/agents/domain.md`
- `examples/USAGE.md`: full command reference, worker contract, safety and recovery
