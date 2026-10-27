# AI Coding Factory — High-Level Architecture Overview

_Snapshot: 2026-10-26. For the target-architecture rationale and the ownership boundaries, see [architecture.md](architecture.md). For handoff status, see [../HANDOFF.md](../HANDOFF.md). Command reference: [../examples/USAGE.md](../examples/USAGE.md)._

Legend: ✅ built in this repo · 🔌 external tool, already integrated · 🕓 planned, not built yet

```
                                   ┌──────────── HUMAN ────────────┐
                                   │ request · answer · approve    │
                                   └──┬──────────────┬─────────────┘
                     /factory-intake  │              │ comments / approve / reject
                                      ▼              ▼
┌─ INTAKE (any agent: Claude Code, ─┐  ┌─ Scotty 0.3.2 🔌 ──────────┐
│  Codex, OpenCode, Copilot) ✅     │  │ board + Needs You inbox    │
│ factory-producer ◄─grill─ grilling│  └────────────┬───────────────┘
│   + domain-modeling,              │               │ same backend
│   codebase-design                 │  ┌────────────▼───────────────┐
│ factory-principal-engineer        │  │ Beads 1.3.1 (bd) 🔌        │
│   → to-spec → to-tickets          │─►│ IDs acf-<N> via bd-new     │
│ optional: prototype · wayfinder · │  │ Dolt DB, refs/dolt/data    │
│   handoff                         │  │ epics·tasks·deps·gates     │
│ decisions → `factory decisions    │  └────────────▲───────────────┘
│   await`                          │               │
└───────────────────────────────────┘               │
┌─ CONTROL PLANE: Go factory (cmd/factory) ✅ ──────┴────────────────────────┐
│ controller · workflow · repo · process · integrity · human_review          │
│ integration · decisions · beads ─► SQLite journal (.git/factory) ✅        │
│ run worktree: <parent>/<repo>.factory-runs/<id> (0700 root, never .git)    │
│ doctor: phase_runtimes (policy + `claude --version`) ✅                    │
│ 🕓 acf-13: unattended daemon + serialized landing queue                    │
│ 🕓 acf-19: live status/controls API next to Scotty                         │
└──────────────┬─────────────────────────────────────────────────────────────┘
               │ argv/cwd/deadline/env, private run artifacts
               ▼
┌─ EXECUTION PLANE ──────────────────────────────────────────────────────────┐
│ `factory agent --policy P` (policy v2) / `factory copilot` (v1 or v2) ✅   │
│ Shared phase runner (RunPhase) → runtime Driver:                           │
│   copilot ✅ (behaviour unchanged; inherits env, acf-35 🕓)                │
│   claude  ✅ (`claude -p`, subscription only: path-scoped tools, exact env │
│              allowlist, init-event isolation check, launch-dir + residue   │
│              audit, failure_reason in adapter-outcome.json)                │
│   codex / opencode 🕓 (acf-23)                                             │
│  Producer → Dev → checks(go test/vet) → QA → Review   in isolated worktree │
│ Roles via APM 🔌 (.apm/agents → .github/.claude/.opencode agents):         │
│  factory-producer, factory-dev, factory-qa, factory-reviewer,              │
│  factory-principal-engineer · upstream awesome-copilot profiles: Copilot   │
│ Conductor v0.1.42 🔌 (optional in-task workflow/checkpoints, foreground)   │
└──────────────┬─────────────────────────────────────────────────────────────┘
               ▼
┌─ REVIEW / EVIDENCE PLANE ──────────────────────────────────────────────────┐
│ deterministic checks ✅ · patch-bound QA/review verdicts ✅                │
│ roborev 🔌: report import (schema-v2 + candidate hash) ✅                  │
│            native restricted launch with provenance 🕓 (acf-16)            │
│ integrity seals → candidate fingerprint ✅                                 │
└──────────────┬─────────────────────────────────────────────────────────────┘
               ▼
  review-publish ─► Scotty human gate ─► review-sync ─► integrate ✅
                                  (lock, baseline HEAD, rerun checks, close task;
                                   no commit/merge/push) ─► authoritative Git

┌─ KNOWLEDGE PLANE 🕓 (acf-12) ──────────────────────────────────────────────┐
│ inputs: repo, AGENTS.md, APM-locked agents/instructions, Matt skills,      │
│         GLOSSARY.md, docs/adr, approved source snapshots/archives          │
│   → obsidian-wiki   curated engineering notes                              │
│   → Memsearch       derived vector index over Markdown (Python, external)  │
│   → Graphify        structural/code relationship graph                     │
│ Go context assembly: bounded, provenance-tagged context into workers       │
│ Go outcome capture: approved outcomes only, no raw logs/transcripts,       │
│   dedupe interactive vs worker capture                                     │
│ today: Memsearch per-repo profiles (personal / work / off) for the user's  │
│   interactive agents only 🔌                                               │
└────────────────────────────────────────────────────────────────────────────┘
 Optional/undecided: OpenSpec or Spec Kit · Not required: tmux/cmux, Agentastic
```

## Where each component fits

- **Phase runtimes** sit in the execution plane behind one shared runner.
  - The runner owns everything runtime-independent: the private launch dir, sealed inputs and profile, evidence, integrity checks and the Dev/QA/Review contracts.
  - Each runtime is a small `Driver`. Copilot's behaviour is pinned byte-for-byte by golden tests. Claude adds stronger isolation that is verified at run time (see [ADR 0001](adr/0001-claude-phase-worker-via-cli.md)).
  - Codex and OpenCode will each be one more driver (`acf-23`).
- **Run worktrees** live beside the repository, at `<parent>/<repo>.factory-runs/<run-id>`, because Claude Code refuses writes under `.git`. The journal and evidence stay under `.git/factory`.
- **roborev** belongs to the review plane.
  - Today the factory only **imports** reports that already exist. A report must be canonical schema-v2 and carry an explicit candidate hash.
  - Running roborev natively is not supported. Its Copilot runner gives blanket tool approval, which would widen the factory's permissions.
  - `acf-16` will add a restricted launch that records where each report came from and how fresh it is.
  - Separately, interactive roborev reviews of this repository use Claude (`.roborev.toml`).
- **Memsearch** is a derived index: it can always be rebuilt from Markdown.
  - It never replaces Beads, the journal or Conductor checkpoints.
  - Today it only backs the user's interactive agents. A per-repository profile chooses the personal store, the work store or none.
  - Bounded worker context from Memsearch is planned under `acf-12`.
- **obsidian-wiki** will hold curated engineering notes built from approved source snapshots. Planned under `acf-12`.
- **Graphify** will provide a structural graph of code relationships, so workers get bounded context. Planned under `acf-12`.
- **Conductor** can run one task's workflow and checkpoints in the foreground.
  - The Go controller owns admission, scheduling and integration.
  - Conductor must never close beads or land code.
- **Beads and Scotty**:
  - Beads is the task authority. Top-level beads are created with `scripts/bd-new` so they get sequential `acf-<N>` IDs.
  - Scotty is the human interface: the board and the Needs You inbox.
  - Neither controls workers or authorizes integration on its own.
- **APM role profiles** provide guidance only. They are not permissions or sandboxes. Each phase's policy (v1 or v2) bounds what that worker can use, and the driver enforces it.

## Built vs planned

| Status | Components |
|---|---|
| ✅ Built | Go CLI and controller, SQLite journal, worktrees outside `.git`, process supervision, supervised workflow, shared phase runner with Copilot and Claude drivers, policy v2 and `factory agent`, doctor `phase_runtimes`, roborev report import, integrity seals, Scotty review gate, approved local integration, `decisions await`, `/factory-intake` skill, neutral pipeline roles |
| 🔌 Integrated | Beads, Scotty, Copilot CLI, Claude Code CLI, Conductor, APM agent profiles, Matt Pocock skills, Memsearch (interactive only), roborev (report import; interactive reviews) |
| 🕓 Planned | Codex/OpenCode drivers (`acf-23`), Copilot env allowlist (`acf-35`), `acf-13` daemon and landing queue, `acf-19` live status and controls, `acf-16` native roborev, `acf-12` knowledge plane (obsidian-wiki, Memsearch for workers, Graphify), risk-based specialist routing, OpenSpec/Spec Kit (undecided) |
