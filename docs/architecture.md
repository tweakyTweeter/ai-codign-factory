# Coding factory architecture

This is the target architecture, not a claim that every integration is already
implemented. The first executable slice is a foreground Go controller with a
durable journal, isolated Git worktrees and external command workers.

```text
                           HUMAN
                     request / approve / stop
                              |
            +-----------------+-------------------+
            |                                     |
       factory CLI                         Scotty task UI
            |                                     |
            v                                     v
  +--------------------------+         +-----------------------+
  | Go coordinator/controller|<------->| Beads (bd + local Dolt)|
  | admission, claims, limits|         | tasks, deps, ownership |
  | reconciliation, policy   |         +-----------------------+
  +-------------+------------+
                |             +-------------------------------+
                +------------>| SQLite execution journal      |
                |             | attempts, states, artifact refs|
                |             +-------------------------------+
                v
  +--------------------------+         +-----------------------+
  | Go runtime adapter       |-------->| Private run artifacts |
  | argv, cwd, deadlines, env |         | context, logs, evidence|
  +-------------+------------+         +-----------------------+
                |
                v
  +--------------------------+
  | Conductor (external)     |
  | task workflow/checkpoints|
  +-------------+------------+
                |
                v
  +-----------------------------------------------------------+
  | Phase runtime drivers (Copilot, Claude), risk profiles    |
  | Producer -> plan/design -> Dev -> independent QA/review     |
  |                                                           |
  | Implementation Plan | Principal Engineer | API Architect  |
  | Debug | TDD Red / Green / Refactor | Governance Reviewer    |
  | DevOps | GitOps CI | Kubernetes SRE | PostgreSQL | Terraform|
  +----------------------------+------------------------------+
                               |
                        isolated Git worktree
                               |
                   checks / QA / roborev evidence
                               |
                      human approval gate
                               |
                   serialized integration queue
                               |
                        authoritative Git

  CONTEXT / KNOWLEDGE PLANE (bounded inputs, approved outcomes)
  +-----------------------------------------------------------+
  | Repository + AGENTS.md + APM-locked agents/instructions     |
  | Optional spec workflow: OpenSpec or Spec Kit (undecided)    |
  | Approved source exports / snapshots / archive              |
  |              -> obsidian-wiki curated engineering notes    |
  |              -> Memsearch derived retrieval index          |
  |              -> Graphify structural/code relationships     |
  | Go context assembly / provenance / safe outcome adapters   |
  +-----------------------------------------------------------+
```

## Ownership boundaries

| Component | Owns | Does not establish |
|---|---|---|
| Beads | Durable work items, dependencies and claims | A running worker or accepted code |
| Go controller | Admission, attempt lifecycle, reconciliation and integration policy | LLM task reasoning |
| SQLite journal | Local execution intent and observed outcomes | Task authority or remote distributed locking |
| Runtime adapter | Process boundary, arguments, deadlines and evidence; Copilot and Claude drivers behind one runner ([ADR 0001](adr/0001-claude-phase-worker-via-cli.md)) | A sandbox or tool authorization |
| Conductor | One task's workflow and checkpoints | Global scheduling or exactly-once side effects |
| Copilot profiles | Role guidance and task-specific expertise | Permissions, approval or independent QA by themselves |
| Git/worktrees | Baselines, isolated changes and integration history | Isolation from the host, network or credentials |
| Scotty | Human-facing Beads visibility and native human-gate decisions | Worker control or integration authority |
| QA/checks/roborev | Review and verification evidence | Automatic authority to commit, merge or push |
| Wiki/Memsearch/Graphify | Curated knowledge and derived retrieval | Source truth or executable instructions |

The controller must not compete with Conductor for task-internal scheduling.
Likewise, a completed Conductor workflow must not independently close a bead or
land code. The factory should fail closed when task ownership, attempt state or
review evidence is ambiguous.

## Current boundary and later stages

The initial implementation deliberately uses synchronous execution instead of
an unattended daemon. Successful execution retains the worktree for review;
it does not commit, integrate, close the task or push.

External Conductor/Copilot and Scotty/Beads connectivity have been exercised.
The Go controller now also supports an explicit supervised phase sequence,
selected specialist stages, Copilot profile snapshots, patch-bound structured
QA/review verdicts and foreground Beads lease renewal. These are controller
capabilities, not a claim of automatic specialist selection or OS isolation.

The explicit review bridge creates a private candidate/evidence bundle and a
native Beads human gate visible in Scotty's Needs You inbox. Decision sync
validates the exact run, fingerprint, request, evidence and task ownership.
Human-review state is separate from execution/release; approval does not land
code or close the task. See [human approval](human-approval.md).

An explicit `integrate` command now serializes local approved-patch application
under the common controller lock. It requires the original baseline HEAD,
preserves unrelated dirty source and index state, reruns the saved checks and
conditionally closes the same owned task before releasing admission. Durable
intent and exact readback markers distinguish known completion from uncertain
apply/check/close outcomes. It creates no Git commit or merge.

Unattended scheduling, a durable landing queue, automatic risk-based routing,
restricted native roborev launch, live factory execution controls in Scotty,
automatic approval watching, and context/knowledge adapters require
additional implementation and acceptance evidence. A user-local interactive
memory hook is not the factory's worker-memory pipeline.

tmux/cmux and Agentastic are not required components. Scotty replaces the
requested visibility layer; process recovery belongs in the controller.
