# Supervised coding workflow

The milestone after the foreground command runner is a supervised coding loop,
not an unattended scheduler or automatic integration service.

Requests can first go through [intake](intake-workflow.md), which grills,
specs and tickets them in Beads. Each resulting ticket then runs through this
workflow unchanged with `factory run --task`.

## Roles

| Stage | Responsibility | Write policy |
|---|---|---|
| Producer | Scope, constraints, acceptance and handoff | No repository writes; no implementation builds |
| Selected specialists | Relevant design or risk assessment | Planning/review only in this workflow |
| Dev | Implement the requested change and tests | Sole repository write phase |
| Checks | Execute the configured deterministic verification | Must preserve the candidate being verified |
| QA | Independently assess behavior and check evidence | No application changes |
| Review | Assess the exact candidate and report findings | No repository writes |
| Human | Inspect the evidence and decide integration | Outside automated execution |

Specialists are explicit, risk-based selections, not a request to run every
installed profile. An API change may need API Architect; schema work may need
PostgreSQL DBA; deployment work may need Kubernetes SRE or Terraform. A profile
that requires unavailable tools or models is not a verified runtime capability.

QA and review use separate sessions from Dev. Independence here means role
and session separation with distinct evidence, not a guarantee that models have
independent failure modes. Deterministic checks and human judgment still matter.

## Evidence and authority

A command's exit code is necessary execution evidence, not a review verdict.
Missing, malformed, unsupported, inconclusive or stale verdict evidence must
block progression. Findings must not be discarded just because an agent also
says "pass".

Verification must bind to the actual candidate, including untracked files,
and detect changes by a non-writing stage. Do not accept QA on one patch and
review on a different patch. These checks detect changes after execution; they
do not sandbox the process or prevent a transient write followed by restoration.

Keep planning output, deterministic check logs, QA evidence and review results
as separate artifacts. An `awaiting_review` run is ready for human inspection,
not approved, integrated or completed in Beads.

The controller journals SHA-256 seals for prompt/config/context/task/claim
inputs (including absent optional inputs), each phase context before launch,
and successful command logs and complete phase output trees before downstream
execution. Seals include names and modes: additions, removals, content/mode
changes, symlinks and unsupported file types fail closed. Individual files are
limited to 1 MiB; each sealed tree to 128 MiB and 100,000 entries.
Seals are checked before and after commands, before the human handoff, and
again during review publication, decision sync and integration. Integration
check logs are sealed too. Existing seals are never refreshed from changed
content. Retained runs without seals require a new verified run for review or
integration; completed integrations remain historical records.

## Ownership and local execution

Task admission uses Beads readiness and an explicit claim. Renewal must use the
same actor that acquired the claim; ownership must be checked before stages and
the final handoff. Lost ownership or an uncertain renewal must stop execution,
retain evidence and require inspection. Never infer permission to retry from
an expired lease.

Beads leases are node-local. A heartbeat is not a distributed lock, and an
unpublished local task mutation is not cross-machine coordination.

Headless tools must have explicit policies. APM profiles supply role guidance,
not filesystem isolation or permission grants. Snapshot approved profiles with
provenance rather than assuming untracked profiles exist in a new worktree.
No automatic blanket tool approval, Git integration or deployment permissions
are part of this milestone.

## Headless profile compatibility

Copilot CLI 1.0.100 supports loading a profile snapshot from an explicitly
supplied directory with `.github/agents/` through `--add-dir`, then selecting it
with `--agent`. This also grants access to the snapshot directory; it is not a
profile-only import or a filesystem sandbox.

Tool availability varies with the selected model. In the exercised Auto
session, a Dev allowlist containing only `create` and `edit` did not expose a
writing tool; adding `apply_patch` enabled an actual verified file write.
Do not interpret an exit-zero response as proof that the requested tool was
available or that a file changed. Validate the artifact and stage outcome.

The read-only Producer and QA profiles were also exercised with only
`view`, `rg` and `glob`; QA inspected the actual marker produced by the
separate Dev session. This was a compatibility probe, not the real-task
acceptance test.

`--no-custom-instructions` alone does not isolate user-global extensions or
hooks. The first real-task run safely failed when an interactive memory hook
emitted a second final answer. The adapter now launches outside any checkout
from a private temporary directory with supported local
`disableAllHooks: true`, retaining the normal user home for authentication and
passing the worktree explicitly. It does not alter global or candidate
configuration. Global extensions can still load.

This exact adapter was then exercised against real Copilot: Producer planned,
Dev implemented a standalone context-reader example in an isolated worktree,
controller tests and vet passed, and separate QA/review sessions accepted the
same candidate fingerprint. The run stopped at `awaiting_review`, with the
candidate retained and no integration.

Subsequently, native Scotty human approval and a separately authorized
`factory integrate` invocation completed the handoff: the exact two files were
applied to the main checkout, saved tests/vet passed there, the task was closed
and admission released without staging, committing or pushing. See
[human approval and integration](human-approval.md) for the explicit commands,
conflict handling and recovery limits.

Restrict model-visible tools explicitly, neutralize inherited auto-approval
environment settings, and keep the trusted-local-process boundary visible.
The installed CLI's `copilot help permissions` documents exact `write(path)`
rules, not recursive directory/wildcard write approval. The adapter does not
invent a recursive rule or claim Dev write approval is path-scoped. Instead it
removes the live run-directory grant, disables implicit system-temp access
with `--disallow-temp-dir`, and exposes only the candidate plus private
input/profile copies under its launch directory. Input JSON path references
are rebased to the copies while candidate paths stay unchanged. Only sealed
inputs and completed outputs are copied, not the journal, current results or
sibling runs. Private copies, launch settings and adapter provenance are
checked for changes before the response is accepted; copies are then removed.

This narrows adapter-granted file access, not arbitrary configured commands or
inherited global settings/extensions. Evidence and journal files are not
tamper-proof against processes with equivalent host privileges, nor are
transient writes followed by restoration detected. QA assesses controller
check evidence rather than rerunning commands.

## roborev compatibility

The investigated baseline is roborev v0.71.0. Its canonical live review document
uses `schema_version: 2`, a nonempty summary, a verdict of `pass`, `fail` or
`unable_to_review`, and a required findings array. Each finding has severity,
problem, fix and a location (which may be null).

Two native behaviors require care:

- Local review process success does not by itself encode a clean verdict.
- Its Copilot runner adds blanket tool approval; native review invocation is
  not evidence of the factory's intended restricted tool policy.

An adapter must validate canonical evidence and enforce its launch policy
explicitly. Do not parse a rendered "No issues found" sentence, silently inherit
user hook mutations, or treat historical/legacy reports as current patch-bound
approval. Unsupported execution modes must fail explicitly, not fall back to
unchecked native execution.
