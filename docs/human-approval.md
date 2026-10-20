# Human approval through Scotty

Scotty's **Needs You** view is the human decision inbox. The factory's review
bridge uses a native Beads **human gate**, not a `human` label on the
implementation task.

## Publishing and checking a request

Publication is explicit; there is no background watcher or automatic approval
consumption:

```sh
factory review --repo /path/to/repository --run RUN_ID
factory review-publish --repo /path/to/repository --run RUN_ID
```

The commands return JSON with the review artifacts and request information.
Use the exact approval token shown by the published request as the entire
**Add approval note** in Scotty, then click **Approve** only after inspecting
the candidate and evidence. Afterwards:

```sh
factory review-sync --repo /path/to/repository --run RUN_ID
factory status --repo /path/to/repository --run RUN_ID
```

The factory records human-review state separately from the execution state.
Sync validates a decision; it does not integrate or release the attempt.
The published request supplies the explicit rejection token and terminal
instructions too. Do not click Approve to reject a change.

To reject, copy `run.human_review.gate_id` and `reject_reason` from the JSON:

```sh
bd gate resolve GATE_ID --reason 'factory-reject:RUN_ID:FINGERPRINT' --sandbox
factory review-sync --repo /path/to/repository --run RUN_ID
```

Beads 1.3.1 prints text from `gate resolve` even with `--json`; the factory
consumes subsequent gate reads, not that command's output. Status is a cached
observation, not a live approval check; run `review-sync` to refresh it.
Changed candidates require a new verified run. If publication reports an
uncertain intent without a discoverable gate, inspect Beads manually rather
than retrying gate creation yourself.

Review, publication, sync and integration also verify the controller's
journaled input/artifact seals, not just hashes captured when the review bundle
was created. Modified planning text, profile snapshots, context, logs or other
completed outputs invalidate the request. A legacy run without these seals
cannot be retroactively blessed by hashing its current files: start a new
verified run. Already completed integrations remain historical results.

## Decision boundaries

The implementation task depends on the approval gate, so the gate waits on the
human, not on the implementation task being closed. The task remains
`in_progress` with its factory owner while review is pending.

Approval means accepting the specific candidate identified by the request's
run and fingerprint. It does not authorize a different patch, mark the task
complete, release the factory's admission hold, integrate files, commit, merge,
push or deploy. Those are separate operations with separate authority.

The bridge must validate both the Beads decision and current local state.
Closed gates without the expected decision marker, edited request metadata,
changed candidates, changed ownership and cancelled runs must not become
accepted approvals. A gate reopening must not leave an old cached approval
usable.

This is a trusted local, single-user workflow. A decision marker is an explicit
binding, not cryptographic proof of who pressed a button. Anyone with equivalent
local Beads/database access can edit decisions. An external controller must not
treat this as an authenticated remote authorization service.

## Explicit integration after approval

Approval alone does not start integration. An operator separately invokes:

```sh
factory integrate --repo /path/to/target-checkout --run RUN_ID
```

The command re-reads the native gate instead of trusting cached approval.
It validates the retained candidate, evidence, task owner/status and blockers,
and requires the target HEAD to equal the candidate's original baseline.
It serializes with other controllers through the common Git-directory lock.
There is no watcher, automatic rebase, commit, merge, push or remote sync.

The approved combined diff is applied without staging. Existing changes on
touched paths (including existing untracked additions) stop integration;
unrelated staged, unstaged and untracked source is preserved. Regular files,
binary additions, deletions, executable modes and leaf symlinks are supported.
Symlink ancestors, directory/file transitions and submodules are refused.

The saved configured checks run with literal argv, their existing deadlines,
and private bounded logs in the retained run directory. `FACTORY_WORKTREE` and
`FACTORY_REPO` identify the target checkout; prompt/context paths still point
to the original run artifacts. Source-visible files (tracked and nonignored
untracked files), explicitly approved ignored paths and index entries must
match the expected application after checks. Source snapshots are bounded to
100,000 files, 128 MiB total and 1 MiB per regular file. Unrelated ignored
runtime files, databases and caches are not covered by this mutation guard.
This remains trusted local execution, not an OS sandbox.

Only after checks and fresh approval validation does the command conditionally
close the original task using its exact assignee and `in_progress` status.
A `factory-integrated:RUN_ID:FINGERPRINT` note is written with that update,
then read back to reconcile a lost response. Successful completion records
`integration.state=complete`, releases admission and retains all artifacts.
It does not clear the assignee. Repeating a completed command returns its
historical result without reapplying or claiming the current checkout is
unchanged.

Failed checks, source/index mutations and uncertain apply/check outcomes retain
the patch and admission hold. They are not rolled back or retried automatically.
A restart after a known applied patch can start checks; a restart after known
checks and an exactly marked task closure can finish release without rerunning
checks. An uncertain close with an open task requires inspection, not another
blind close attempt. Cancellation or ownership/approval loss prevents success;
cross-database completion is not an atomic transaction, so a task closed just
before a later failure can remain closed while the run stays held.

Inspect the integration snapshot, logs, target, task and any surviving processes
before manual recovery. If abandoning that partial integration, explicit
`release --ack-orphan --reason '...'` acknowledges inspection and releases the
hold; it does not repair or undo files, reapprove changes or complete the task.

## What Scotty displays

Scotty 0.3.2 renders a ready human gate's full Markdown description directly in
**Needs You**. A review request should include the exact candidate identity,
check and QA/review summaries, a diff preview and the local full-evidence
location. New untracked files must appear as additions, not disappear as they
do in an ordinary `git diff`.

Local `file://` links are deliberately filtered by Scotty's Markdown renderer.
Use the displayed terminal command to inspect the full local artifacts.
Publishing into Beads persists the displayed preview in its database; do not
publish secrets or sensitive raw logs. There is no external upload or new
network listener required by the bridge.

The native **Approve** button closes the gate with a reason beginning
`Approved by ... via Scotty at ...`. It offers **Add approval note**.
The factory requires its exact candidate-bound approval marker in that note;
a plain closure or an unrelated comment is not sufficient.

Scotty has no dedicated rejection action on that gate card. A rejection must
use the bridge's documented explicit rejection marker and closure path, not
the Approve button. Do not interpret Dismiss on a separate human-needed bead
as rejecting or approving a factory patch.

## Clarification is different from approval

Separate human-needed beads are useful when an agent needs a decision,
clarification or help with a blocker. They are not patch approval gates.

The installed Scotty UI posts an answer as a comment and removes the `human`
label without closing the bead. This deliberately differs from the installed
`bd human respond` CLI, which comments **and closes** its bead. Avoid using that
CLI on the implementation task merely to answer a question.
