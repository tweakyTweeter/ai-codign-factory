---
name: factory-intake
description: Turn a loose task request into a grilled, Principal-Engineer-reviewed spec and Beads tickets, then run each ticket through the supervised factory pipeline. Use when the user invokes /factory-intake or asks to invoke a task through the factory. Human-only decisions go to Scotty Needs You and the flow resumes once they are answered.
argument-hint: "The task or feature request to take through intake"
---

# Factory Intake

Intake sits **in front of** the existing supervised factory. It does not
replace or modify any agent profile, skill, workflow config or controller
policy. It produces a spec epic and ready tickets in Beads, then hands each
ticket to `factory run --task` exactly as an operator would.

Read first: `docs/intake-workflow.md`, `docs/agents/issue-tracker.md`,
`docs/agents/domain.md`, `docs/supervised-workflow.md`, `docs/human-approval.md`.

## Hard rules

- Beads is the tracker. Follow `docs/agents/issue-tracker.md` for every
  publish, fetch, claim and close. Add `--sandbox` to every `bd` call.
- Intake never edits the repository working tree. Glossary, ADR and
  prefactor changes become tickets that go through the factory.
- The grilled subagent never speaks for the human. Questions of preference,
  scope, risk acceptance, policy, cost or priority become decision beads
  (see "Ask the human"). Never answer them yourself.
- One admitted factory run at a time. Never bypass review approval. Never
  commit, push or sync Dolt remotes, and do not use `/implement` or
  `/implement-spec` inside this flow (the factory replaces them).
- Stop and report on any failed, held, rejected, dismissed or ambiguous
  outcome. Do not retry automatically.

## Ask the human

When a decision cannot be settled from code, docs or the subagent:

1. Create a child decision bead on the intake epic:
   `bd create "<question>" --parent EPIC -t decision -l human,factory-intake --body-file FILE --silent --sandbox`
   (FILE under `$TMPDIR`). The body states the question, the options with
   trade-offs, the recommended answer and what it unblocks. It renders in
   Scotty **Needs You**; the human answers by adding a comment there.
2. Batch independent questions, then block on all of them:
   `factory decisions await --ids ID1,ID2 --poll 15s`
3. On `all_answered: true`, treat each comment text as the human's answer.
   Relay it to the subagent, record it on the epic
   (`bd update EPIC --append-notes "<decision>: <answer>" --sandbox`) and
   close the decision (`bd close ID --reason "Answer recorded on EPIC" --sandbox`).
4. On a non-zero exit (dismissed, cleared without an answer, timeout), stop
   and report which decisions need attention.

Never comment on a decision bead yourself: any comment counts as the answer.

## Phases

Create and claim the intake epic first:
`bd create "<request title>" -t epic -l factory-intake --body-file REQUEST --silent --sandbox`
then `bd update EPIC --claim --sandbox`.

### 1. Draft (Producer subagent)

Launch an `ai-team-producer` subagent and keep its agent ID for follow-ups.
Give it the raw request, the repository path and the read-first docs. Ask it
to explore the code and return: problem statement, proposed scope, affected
modules, test seams it would use, open assumptions, and which assumptions
only the human can decide.

### 2. Grill the subagent

Call the Skill tool with `grill-with-docs` if installed, otherwise
`grill-me` (which delegates to `grilling`). Also call `domain-modeling` and
`codebase-design` to frame the questions. If the grilling skill you need is
not installed, tell the user which one is missing and stop.

Run the interview **against the subagent** with `write_agent`, one question
at a time, walking the decision tree until every branch is resolved. Push on
vague terms (domain-modeling), interface depth and seam placement
(codebase-design), failure modes, the ownership and authority boundaries in
`docs/architecture.md`, and testability. Questions the subagent cannot answer
from evidence go to "Ask the human". Do not let the subagent invent human
preferences.

Optional branches:
- **prototype** when "how should it behave or look" is the open question:
  call the Skill tool with `prototype`, build only under `$TMPDIR`, and link
  it from the epic notes.
- **wayfinder** when the request is too large for one spec: call the Skill
  tool with `wayfinder`, chart the map in Beads per the tracker doc, then
  stop. Run intake again per resolved destination.

### 3. Principal Engineer review

Launch a `Principal software engineer` subagent with pointers to the epic,
the grilling outcome and the human answers. Ask it to challenge the design
for cross-cutting risk, depth and seam placement (codebase-design
vocabulary), failure handling and test strategy, and to return required
changes. Resolve each required change with the Producer subagent or through
"Ask the human".

### 4. Spec

Call the Skill tool with `to-spec`. Synthesize; do not re-interview. Confirm
the test seams through one "Approve spec seams" decision (seams in the
body). Publish by replacing the epic description:
`bd update EPIC --body-file SPEC --sandbox`. Do not label the epic
`ready-for-agent`; only tickets carry that label.

### 5. Tickets

Call the Skill tool with `to-tickets` on the epic. Instead of quizzing in
chat, put the numbered breakdown (title, blocked by, what it delivers,
acceptance criteria) in one "Approve ticket breakdown" decision and iterate
through "Ask the human" until approved. Then publish children in dependency
order per the tracker doc. Glossary/ADR updates and prefactors come first as
their own tickets.

### 6. Run the factory sequentially

Loop until no ticket remains or something stops the loop:

1. Frontier:
   `bd ready --parent EPIC -l ready-for-agent --unassigned --json --limit 0 --sandbox`.
   Take the first result. If none is ready but open tickets remain, report
   their blockers and stop.
2. Write the ticket title, description and acceptance criteria to a prompt
   file under `$TMPDIR` (never in the repository).
3. `factory run --task ID --prompt-file PROMPT`. A non-success result stops
   the loop.
4. `factory review-publish --run RUN`, then tell the user the approval gate
   is in Scotty Needs You. Every 30s run `factory review-sync --run RUN`:
   `human_review.decision == "approved"` continues; `"rejected"`, or a
   `detail` beginning `closed gate observed` without approval, stops.
5. `factory integrate --run RUN`. Integration closes the ticket and releases
   admission. Any held or ambiguous result stops the loop.

After the last ticket closes, close the epic with a summary reason.

### 7. Handoff

If context runs long, the loop stops, or the session ends, call the Skill
tool with `handoff` (written to `$TMPDIR`). Point at the epic ID, open
decisions, the current run ID and the next command. Reference Beads IDs
instead of pasting spec or ticket bodies.

## Report

End with: epic ID, decisions and answers, ticket IDs with status, run IDs,
what stopped (if anything) and the exact next command.
