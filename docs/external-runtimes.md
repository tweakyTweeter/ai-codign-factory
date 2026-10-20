# External runtimes

All new factory backend code is Go. Conductor and Memsearch are external
Python-based tools, allowed by the agreed language boundary. Scotty and Copilot
are external applications, not backend services reimplemented by this project.

## Conductor

The compatibility baseline exercised during development is Conductor v0.1.42.
Install a reviewed, pinned release using the upstream installation instructions:
<https://github.com/microsoft/conductor>.

The official v0.1.42 wheel is:

```text
https://github.com/microsoft/conductor/releases/download/v0.1.42/conductor_cli-0.1.42-py3-none-any.whl
SHA-256: a04b02c8d8ed8984eb87223b033b718b61d0a10aa1ca62bf5109a5b57afeb755
```

This release was not available as `conductor-cli==0.1.42` from the configured
package index; the verified official release wheel was installed instead.
Do not bypass network policy or TLS verification if installation is blocked.

Two non-writing probes are included:

```sh
CONDUCTOR_NO_UPDATE_CHECK=1 conductor validate workflows/runtime-smoke.yaml
CONDUCTOR_NO_UPDATE_CHECK=1 conductor validate workflows/copilot-probe.yaml
CONDUCTOR_NO_UPDATE_CHECK=1 conductor --silent run workflows/runtime-smoke.yaml --no-interactive
CONDUCTOR_NO_UPDATE_CHECK=1 conductor --silent run workflows/copilot-probe.yaml --no-interactive
```

The first runs `go version` without an LLM. The second makes a bounded Copilot
provider call with no tools and expects `FACTORY_COPILOT_OK`. These establish
external runtime/provider connectivity, not safe autonomous implementation.

Keep Conductor in the foreground under factory supervision. Do not use
`--web-bg`, `--skip-gates`, or another launcher that exits before its worker:
an exit code from a detached launcher is not workflow completion.

Conductor's default provider is Copilot. Its provider-backed agents do not
automatically select the repository's APM-installed agent profiles. Explicit
profile mapping, instruction provisioning, tool restrictions and permission
behavior must be verified before admitting an implementation workflow.
Conductor's own resume/checkpoints also do not establish exactly-once external
side effects. Reconcile a failed factory attempt before deciding to resume it.

## Beads and Scotty

The compatibility baseline exercised during development is Beads 1.3.1 and
Scotty 0.3.2. A transient loopback-only Scotty instance successfully read the
factory's real Beads tasks through the project-scoped API.

```sh
POSTHOG_KEY='' SCOTTY_READ_ONLY=1 \
  BEADS_REPO="$PWD" scotty --no-open --host 127.0.0.1
```

An empty `POSTHOG_KEY` disables the documented usage-statistics reporting.
Read-only mode is a browser preference, not authentication or authorization;
keep this single-user application local. Review update-check network behavior
and application settings independently of telemetry.

In Scotty 0.3.2 the task endpoint is
`/api/p/<projectId>/beads`, not `/api/beads`.
The project picker is `/api/projects`. Always confirm the selected project
and live-data mode: a healthy demo board does not prove Beads connectivity.

Scotty is the task UI, not a process supervisor. Its board mutations do not
cancel workers, prove checks passed, or authorize integration. Use the same
Beads backend as the factory, and reconcile human edits at execution gates.
Its Claude Code AI-refinement path is not part of this Copilot-based factory.

## Copilot profiles and roborev

See [agent setup](agent-setup.md) for the locked APM profile inventory.
Profiles are prompt guidance, not permission isolation or sandboxes. Some
reference models, VS Code tools, database extensions or Terraform MCP tools
that may not exist in a headless worker.

Use a bounded workflow and one implementation owner per write phase.
Producer scopes/triages; Dev implements; QA independently verifies; specialists
are selected when relevant. Installing profiles does not wire this routing.

roborev supports review of dirty changes and foreground local execution, but
the native command below is a **manual interface reference**, not a supported
restricted factory launch policy:

```sh
roborev review --dirty --local --agent copilot
```

Its output needs assessment; process success alone is not an approval verdict.
The factory's `roborev-import` adapter validates canonical v2 report JSON and
requires an explicit reviewed candidate hash. Report provenance is the trusted
caller's responsibility. Native roborev's Copilot runner adds blanket tool
approval, so automatic native execution remains unsupported rather than
silently widening permissions. See [supervised workflow](supervised-workflow.md).
No new post-commit hooks, commits or remote operations are required by the MVP.

## Memory and source knowledge

Existing interactive Copilot capture, recall and recap are user-installed
integrations, not automatically inherited by independent factory workers.
Worker evidence may contain confidential data: do not automatically feed raw
logs, code, credentials or transcripts into a shared index.

Factory-owned outcome capture, bounded context assembly, source ingestion,
obsidian-wiki curation and Graphify integration remain separate capabilities.
Memsearch indexes are derived from Markdown; neither vector search nor wiki
notes replace Beads, the factory journal or Conductor checkpoints.
