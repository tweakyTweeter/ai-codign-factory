# Agent profiles (APM)

This repository uses APM to install two kinds of custom agent profiles:

- **Project roles** in `.apm/agents/` (`factory-producer`, `factory-dev`,
  `factory-qa`, `factory-reviewer`, `factory-principal-engineer`), deployed to Copilot, Claude Code and
  OpenCode.
- **Selected upstream [Awesome Copilot](https://awesome-copilot.github.com/agents/)
  profiles**, deployed to Copilot only.

These are custom agent profiles, not skills or slash commands.

## Install

Prerequisites: an authenticated Copilot CLI, APM, and access to the public
`github/awesome-copilot` repository. This setup was verified with APM 0.32.0
and Copilot CLI 1.0.100.

From the repository root:

```bash
apm install
```

`apm.yml` selects the `copilot`, `claude` and `opencode` targets and declares
the upstream agents under `dependencies.apm`, each restricted with
`targets: [copilot]`. Their frontmatter uses Copilot tool names, MCP blocks and
models (`gpt-4o`, `GPT-5`); OpenCode refuses to load its configuration when an
agent declares `tools` as a list, and Claude cannot honour those tools or
models. The upstream Kubernetes instructions are Copilot-only for the same
reason: they address Copilot and would load into every Claude session. A top-level `apm:` list is not a dependency
declaration: APM may report that everything is up to date while installing
nothing.

APM deploys profiles under `.github/agents/` (and project roles also under
`.claude/agents/` and `.opencode/agents/`) and records the resolved
upstream commit and content hashes in `apm.lock.yaml`. Keep the manifest,
lockfile, and deployed profiles in version control. `apm_modules/` is an ignored
local dependency directory.

To reproduce the locked installation:

```bash
apm install --frozen
```

The manifest does not pin tags or SHAs, so APM warns that dependencies are
unpinned. The lockfile retains resolved commits for ordinary/frozen installs;
an explicit dependency update or resolving without that lockfile can select
new upstream content. Review agent changes before adopting them. Do not
hand-edit deployed profiles, since APM manages their content.

## Project-local profiles

`.apm/agents/` holds this repository's own, agent-neutral roles. APM treats
them as local primitives: `apm install` deploys them to every target and
records them under `local_deployed_files` in `apm.lock.yaml`. Keep their
frontmatter to fields every harness accepts (`name`, `description`,
`mode: subagent`). Edit the `.apm/agents/` source (a real file, not a
symlink, since APM skips symlinks) and rerun `apm install`; never edit a
deployed copy.

## Installed profiles

In Copilot, use `/agent` to browse the display names. The project roles are also deployed to Claude Code and OpenCode. The file identifiers are:

| Area | Profile identifiers |
| --- | --- |
| Factory intake (project-local, from `.apm/agents/`) | `factory-producer`, `factory-principal-engineer` |
| Factory pipeline phases (project-local; the `source_profile` of `factory agent` policies) | `factory-producer`, `factory-dev`, `factory-qa`, `factory-reviewer` |
| Team | `ai-team-dev`, `ai-team-qa` |
| Planning and architecture | `implementation-plan`, `api-architect` |
| Governance and debugging | `agent-governance-reviewer`, `debug` |
| Operations | `devops-expert`, `platform-sre-kubernetes`, `se-gitops-ci-specialist` |
| Data and infrastructure | `postgresql-dba`, `terraform` |
| Test-driven development | `tdd-red`, `tdd-green`, `tdd-refactor` |

Profiles retain upstream model and tool declarations. Installation does not
guarantee every declared model or tool exists in Copilot CLI: several profiles
reference VS Code tools, PostgreSQL references extension tools, and Terraform
references `terraform/*` MCP tools. Governance Reviewer pins `gpt-4o`, and
SE DevOps/CI pins `GPT-5`; model availability depends on your environment.
No extensions, MCP servers, credentials, database connections, cloud resources,
or model subscriptions are installed by these profile dependencies. Review
available capabilities before using a specialist; unsupported tools may be
ignored by the runtime.

## Installed instructions

APM also installs
`github/awesome-copilot/instructions/kubernetes-deployment-best-practices.instructions.md`
into `.github/instructions/`. This is an instruction file, not an agent or skill;
it does not appear as a selectable `/agent` profile.

The upstream file retains `applyTo: '*'`, so its Kubernetes deployment guidance
is repository-wide rather than restricted to Kubernetes manifests. It covers
workloads, networking, configuration, probes, resources, scaling, and security
contexts. Installation does not provision Kubernetes tools or authorize cluster
changes. Keep the APM-managed file intact and review upstream updates through
the same lockfile workflow as the agents.

## Select the agent

Start a fresh Copilot CLI session from this repository:

```bash
copilot
```

Select it with:

```text
/agent factory-producer
```

Alternatively, start directly with the profile:

```bash
copilot --agent factory-producer
```

Example request:

```text
Clarify the scope of this feature and prepare acceptance criteria and a
handoff for implementation. Follow the repository's Beads workflow.
```

The Producer drafts scope, affected modules, test seams and open
assumptions. Its profile is read-only and prohibits writing code or speaking
for the human. Selecting it does not start Dev or QA or create an autonomous
scheduler; `/factory-intake` orchestrates it. Other profiles have their own implementation and
execution roles; installing them does not execute those workflows.

Existing repository instructions still apply: use Beads for durable work and
do not commit, push, merge, or perform Dolt remote sync without explicit
authority. The profile's reference to merging does not grant that authority.
These behavioral boundaries are prompt guidance, not a sandbox or a tool
permission restriction.

## Troubleshooting

- If APM detects zero dependencies, check the `dependencies.apm` nesting.
- Use `apm install --dry-run --verbose --target copilot` to inspect the declared
  dependency and `apm targets` to inspect target selection.
- If the agent does not appear, verify the deployed file exists, run Copilot
  from this repository, and start a fresh CLI session before selecting it.
- Stop on installation collisions or security warnings; do not use `--force`
  or bypass policy just to make installation succeed.

Do not run `apm compile` for this installation: the agent primitive is deployed
by `apm install`, and the existing repository instruction files must remain
intact. No global agent installation or plugin change is required. The Copilot
target uses `.github/agents/`, which IDE Copilot may also recognize when this
repository is open.
