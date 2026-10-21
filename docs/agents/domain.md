# Domain Docs

How the engineering skills consume this repository's domain documentation.
Layout: single-context.

## Before exploring, read these

- `GLOSSARY.md` at the repository root.
- `docs/adr/`: ADRs touching the area being changed.
- `docs/architecture.md`: ownership and authority boundaries (controller,
  Conductor, Beads, Git, knowledge layers).

If a file does not exist, proceed silently. `/domain-modeling` creates
glossary entries and ADRs lazily when terms or decisions are resolved. During
factory intake, those changes are written as tickets and land through the
supervised factory, not edited directly.

## Use the glossary's vocabulary

Name domain concepts with the glossary's terms in tickets, specs, tests and
proposals. A missing concept is either invented language (reconsider) or a
real gap (note it for `/domain-modeling`).

## Flag ADR conflicts

If output contradicts an ADR or `docs/architecture.md`, say so explicitly
rather than silently overriding it.
