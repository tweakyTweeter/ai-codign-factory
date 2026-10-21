# Intake workflow

Intake turns a loose request into an approved spec and dependency-ordered
Beads tickets, then feeds those tickets one at a time into the existing
supervised factory. It adds a front stage; it does not change the
controller, workflow phases, agent profiles or approval rules.

Invoke it in a Copilot session at the repository root:

```text
/factory-intake <task or feature request>
```

The procedure lives in `.agents/skills/factory-intake/SKILL.md`. The Beads
mapping used by the Matt Pocock engineering skills lives in
`docs/agents/issue-tracker.md` and `docs/agents/domain.md`.

## Flow

```text
request
  → intake epic (Beads, label factory-intake)
  → Producer subagent drafts scope, seams, assumptions
  → grill the subagent (/grill-with-docs or /grill-me, plus
    /domain-modeling and /codebase-design)
      ↳ human-only questions → decision beads → Scotty Needs You
  → Principal Engineer subagent review
  → /to-spec → spec stored as the epic description (seams confirmed via Needs You)
  → /to-tickets → breakdown approved via Needs You → ready-for-agent children
  → for each ready ticket, in order:
      factory run --task → review-publish → Scotty approval → integrate
```

Optional: `/prototype` (throwaway work under `$TMPDIR`) when behaviour is
the open question; `/wayfinder` when the request is too large for one spec
(intake stops after charting the map); `/handoff` when the session must end
mid-flow.

## Human decisions

Anything that only the human can decide (preference, scope, risk, policy,
cost, priority) becomes a child bead of type `decision` labelled
`human,factory-intake`. Scotty shows it in **Needs You**. Add a comment to
answer; the comment is taken verbatim as the decision.

The agent blocks on:

```bash
factory decisions await --repo . --ids ID1,ID2 [--poll 15s] [--timeout 0]
```

It only reads Beads. It returns `0` with `all_answered: true` and each
decision's comments once every listed bead has a comment and either lost its
`human` label (Scotty answer) or was closed (`bd human respond`). A dismissed
bead, or one cleared or closed without a comment, returns non-zero with that
state, and intake stops. Timeout `0` waits until interrupted. `beads_argv`
comes from the factory config when present, otherwise `bd`.

Answering a decision is not patch approval. Patch approval stays the
candidate-bound native gate in `docs/human-approval.md`.

## How the Matt Pocock skills map onto the factory

| Skill | Role in this repository |
| --- | --- |
| `/grill-me`, `/grill-with-docs` | Interview the Producer subagent; human-only branches go to Needs You |
| `/domain-modeling` | Sharpen terms; glossary/ADR edits become tickets |
| `/codebase-design` | Seam and interface vocabulary for grilling and Principal Engineer review |
| `/to-spec` | Spec written into the intake epic |
| `/to-tickets` | `ready-for-agent` child tickets with native blocking edges |
| `/prototype` | Optional; artifacts stay in `$TMPDIR` |
| `/wayfinder` | Optional; map epic labelled `wayfinder:map` |
| `/handoff` | Cross-session continuation document in `$TMPDIR` |
| `/implement-spec`, `/implement` | Not used inside intake. The factory replaces them: one admitted run per repository, human-approved integration, no automatic commits |

`/grill-me` delegates to a `grilling` skill. `/grill-with-docs`, `grilling`
and `/setup-matt-pocock-skills` must be installed separately. If
`/setup-matt-pocock-skills` is run later, keep the Beads tracker choice
("Other") so these files are not replaced with a GitHub or `.scratch`
contract.

## Safety properties kept from the factory

- Intake never edits the working tree, commits, pushes or syncs Dolt remotes.
- Subagents never answer human-only decisions.
- Tickets run strictly one at a time; any failure, rejection, hold or
  ambiguous outcome stops the loop without retry.
- Each ticket still needs its own Scotty patch approval and explicit
  `factory integrate`.
