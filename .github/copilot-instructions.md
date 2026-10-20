# GitHub Copilot Instructions

This repository uses **Beads (bd)** for issue tracking.

## Core Workflow

- Use `bd ready` to find unblocked work
- Use `bd create` to track new work
- Use `bd update <id> --claim` before starting
- Use `bd close <id>` when work is complete
- Treat commit, push, and Dolt remote sync as policy-controlled handoff actions
- Do not commit, push, or run Dolt remote sync unless explicitly authorized

## Context Loading

Run `bd prime` for the full workflow context.

If the Beads Copilot plugin is installed, Copilot CLI will automatically run
`bd prime` on session start and before compaction.
