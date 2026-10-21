# AI Coding Factory

A local-first coding factory with a Go control plane, Beads task tracking,
isolated Git worktrees and external command workers. The current MVP runs one
foreground attempt at a time, records durable evidence, and stops at human
review. It does **not** autonomously land code.

## Start

Requires Go matching `go.mod`, Git, and a trusted configured worker. Beads is
required only for `--task`; Conductor, Copilot and roborev are external tools,
not prerequisites for building the controller.

```sh
go build -o /tmp/factory ./cmd/factory
/tmp/factory init --repo /absolute/path/to/repository
```

Edit the generated `COMMON_GIT_DIR/factory/config.json`, replacing the worker
placeholder and configuring meaningful checks. See
[`examples/factory.json`](examples/factory.json) for the strict JSON schema.
Use absolute paths for wrappers and external workflow files.

```sh
/tmp/factory doctor --repo /absolute/path/to/repository
/tmp/factory run --repo /absolute/path/to/repository --prompt-file /absolute/path/to/task.txt
/tmp/factory status --repo /absolute/path/to/repository
/tmp/factory status --repo /absolute/path/to/repository --run RUN_ID
```

Workers run in a fresh worktree of the selected checkout's committed HEAD.
They read `FACTORY_PROMPT_FILE` and `FACTORY_CONTEXT_FILE`; the controller
does not invent CLI prompt flags or interpolate shell expressions. Uncommitted
source, untracked agent profiles and configuration are not copied. Until the
factory itself is committed with explicit authorization, a worktree of this
repository will not include its new source files.

`--task BEAD_ID` requires a ready, open, unassigned Beads task and claims it
before starting. Do not pre-claim a task intended for this admission path.
`--profile NAME` selects an explicitly configured alternative command, not an
automatically discovered Copilot specialist. In a supervised workflow config,
it instead selects an explicitly declared specialist stage.

For the supervised Producer -> Dev -> checks -> QA -> review pipeline, start
with [`examples/supervised.json`](examples/supervised.json) and its explicit
role-policy files. Replace every placeholder with an absolute path.
[`examples/USAGE.md`](examples/USAGE.md) describes the adapter and verdict
contracts; [supervised workflow](docs/supervised-workflow.md) explains the roles
and trust boundaries. Ordinary worker/check configs continue to work.

## Review and recovery

Successful worker and check commands leave an `awaiting_review` run, its logs,
branch and worktree. Failed and cancelled attempts also retain evidence and
block further admission. Inspect `status --run` for the worktree and logs.

```sh
/tmp/factory cancel --repo /absolute/path/to/repository --run RUN_ID
/tmp/factory reconcile --repo /absolute/path/to/repository
/tmp/factory release --repo /absolute/path/to/repository --run RUN_ID \
  --reason "Reviewed; retained for manual integration"
```

Cancellation is a durable request, not an immediate teardown guarantee.
Reconciliation never kills a stored PID or relaunches an ambiguous attempt.
A `needs_attention` attempt requires manual orphan/ownership inspection and
`release --ack-orphan`. Release acknowledges an attempt; it does not approve
code, integrate, delete evidence, close/unclaim a bead, commit or push.

For supervised task runs, publish a candidate-bound human gate in Scotty's
**Needs You** inbox:

```sh
/tmp/factory review --repo /absolute/path/to/repository --run RUN_ID
/tmp/factory review-publish --repo /absolute/path/to/repository --run RUN_ID
/tmp/factory review-sync --repo /absolute/path/to/repository --run RUN_ID
```

Inspect the diff and evidence before adding the exact approval token in
Scotty's **Add approval note** and clicking **Approve**. Publication and sync
are explicit; neither releases nor integrates the candidate. See
[human approval](docs/human-approval.md) for rejection and trust boundaries.

After separately authorizing local integration, run:

```sh
/tmp/factory integrate --repo /absolute/path/to/target-checkout --run RUN_ID
```

This revalidates approval and ownership, applies only the approved patch,
reruns the saved checks in the target, and closes the task and releases the run
only after success. Conflicts or uncertain outcomes stop with evidence retained.
It does not stage, commit, merge or push. The target HEAD must still equal the
candidate's baseline; unrelated dirty source/index changes are preserved.

**Trusted local execution, not a sandbox:** workers inherit host access,
environment and credentials. Process-group teardown does not contain a
deliberately daemonizing process. No blanket Copilot tool permissions are
added. Claimed attempts check ownership and renew node-local Beads leases while
running. Renewal stops at foreground exit; verify ownership before integration.
Controller-journaled evidence seals detect changed inputs and completed
artifacts. Copilot reads private evidence copies without a live run-directory
grant; this is narrower adapter access, not host isolation.

## Scope and documentation

| Available now | Not yet automated |
|---|---|
| CLI, strict config, SQLite transition journal | Background daemon and unattended scheduling |
| Common-Git-directory admission lock | Cross-machine coordination |
| Worktrees, literal argv, deadlines, bounded logs | Sandbox and deployment permissions |
| Cancellation, conservative recovery, review hold | Unattended landing queue |
| Beads preflight/claim and foreground lease renewal | Distributed ownership |
| Explicit specialist selection and Copilot profile snapshots | Automatic risk classification and knowledge adapters |
| Patch-bound QA/review JSON verdict gates | Native restricted roborev execution |
| Evidence seals and private Copilot input copies | Tamper-proof evidence or OS write isolation |
| Native Scotty human gates and explicit decision sync | Automatic publication and decision watching |
| Explicit approved-patch integration, checks and task completion | Unattended integration queue and automatic rebasing |
| Skill-driven intake (`/factory-intake`) and `decisions await` for Needs You answers | Intake loop as a Go command; tamper-proof decision provenance |

The original runner was exercised in a disposable repository with an actual Go
worker and exact-output check, persisted review hold and duplicate rejection,
then foreground Conductor and a tool-free Copilot provider probe. This is
bounded dogfooding, not an autonomous self-modification claim.

The supervised pipeline also completed a real Beads task through Producer,
Dev, controller tests/vet, independent QA and a separate review session. It
generated a standalone context-reader example in a retained worktree and
stopped at `awaiting_review`. After explicit human approval and separate
integration authorization, `integrate` applied the exact two files to the main
checkout, reran their tests/vet, closed the task and released the run. No commit
or push was made.

- [CLI, config, worker contract and recovery](examples/USAGE.md)
- [Complete target architecture and ownership boundaries](docs/architecture.md)
- [External runtime compatibility and integration limitations](docs/external-runtimes.md)
- [Supervised roles, evidence and permission boundaries](docs/supervised-workflow.md)
- [Scotty human approval and candidate-bound decisions](docs/human-approval.md)
- [Intake: grill, spec and ticket a request before running it](docs/intake-workflow.md)
- [APM-managed specialist profiles and instructions](docs/agent-setup.md)

Development checks:

```sh
go test -race ./...
go vet ./...
go build ./cmd/factory
```

Use Beads for durable work. Commits, pushes, merges and Dolt remote sync require
explicit authorization.
