# Issue tracker: Beads

Issues, specs and wayfinding maps for this repository live in Beads (`bd`).
Beads is the only durable tracker; do not create `.scratch/` tickets or
GitHub issues. Add `--sandbox` to every `bd` call so no Dolt remote sync is
triggered. Write long bodies to a file under `$TMPDIR` and pass
`--body-file`; never write tracker content into the repository.

## Conventions

- **Spec**: an epic. Intake creates it with label `factory-intake`; the spec
  is its description.
- **Ticket**: a child of the spec epic (`--parent EPIC`), type `task` or
  `feature`, label `ready-for-agent`, acceptance criteria in `--acceptance`.
- **Blocking**: native dependencies. `--deps blocked-by:ID` on create, or
  `bd dep add BLOCKED BLOCKER`.
- **Human decision**: a child of the epic, type `decision`, labels
  `human,factory-intake`. It appears in Scotty **Needs You**; the answer is
  the human's comment. Wait with `factory decisions await --ids ...`.
- **Comments**: `bd comments add ID -f FILE`.
- Triage: `ready-for-agent` is the only triage label in use. The spec epic
  and decision beads never carry it.

## When a skill says "publish to the issue tracker"

- Spec: `bd update EPIC --body-file SPEC` (or `bd create TITLE -t epic -l factory-intake --body-file SPEC` when no epic exists).
- Tickets, in dependency order (blockers first):
  `bd create TITLE --parent EPIC -t task -l ready-for-agent --body-file BODY --acceptance "..." --deps blocked-by:ID --silent`

## When a skill says "fetch the relevant ticket"

`bd show ID --json` and `bd comments ID --json`.

## Frontier and closing work

- Frontier: `bd ready --parent EPIC -l ready-for-agent --unassigned --json --limit 0`.
  Plain `bd ready` also lists the epic and answered decisions, so always filter.
- Implementation tickets are claimed and closed by the factory
  (`factory run --task`, then `factory integrate`), never by hand.
- Never close or edit a parent epic while children remain open.

## Wayfinding operations

Used by `/wayfinder`.

- **Map**: an epic labelled `wayfinder:map`; the map body is its description
  (`bd update MAP --body-file ...`).
- **Child ticket**: `bd create QUESTION --parent MAP -t decision -l wayfinder:<research|prototype|grilling|task>`.
  HITL tickets also get label `human` so they appear in Scotty Needs You.
- **Blocking**: native dependencies, as above.
- **Frontier**: `bd ready --parent MAP --unassigned --json --limit 0`.
- **Claim**: `bd update ID --claim` before any work.
- **Resolve**: `bd comments add ID -f ANSWER`, `bd close ID --reason "Resolved"`,
  then add the gist and ID to the map's Decisions-so-far.
