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
Open the gate in Scotty's **Needs You** view; its description is laid out as
described in [The gate description](#the-gate-description). Click **Approve**,
choose **Add approval note**, and paste the approval token from the gate's
**What to do** section as the note, only after inspecting the
candidate and evidence. Clicking **Approve** without that note is recorded as
not approved. Afterwards:

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

## The gate description

`factory review-publish` writes the gate description once, persists it with the
publish intent, and on every later sync requires the native gate to match it
byte for byte. The tokens and the decision rules are unchanged; only the
layout is designed for a human. It is plain language first: what you are
deciding, what the change does, whether it passed, and what to do. Technical
detail comes last. Sections appear in this order:

1. **Title line**: `Approval needed: TASK - TITLE`.
2. **What you're deciding**: fixed sentences saying the decision is whether
   this AI-made change may be applied to your checkout, and that approving does
   not commit or push anything.
3. **In plain words**: Dev's `explain` fields (what changed, why, risk),
   labelled *Written by the AI developer; check it against the diff*. If Dev
   gave none (Copilot, or a run from before acf-73), the section says "The
   developer gave no plain-language summary" and points to the reports under
   **Technical details**.
4. **Result**: each configured check named by what it ran, for example
   "`go test ./...` passed", then QA and Review in words ("passed"), then
   up to three reviewer notes, most severe first, with a plain severity word
   (**Warning**, **Note**) and labelled as written by the AI reviewers. Further
   notes are counted, with a pointer to **Technical details**. A check's name
   is its argv from the run's sealed configuration, with absolute paths
   shortened to their base name and at most 120 bytes shown.
5. **What changed**: file and `+`/`-` line counts for product code, tests,
   docs and config. Groups come from a path heuristic for orientation only:
   test files and `testdata` are tests; Markdown, `docs/` and README-style
   files are docs; JSON/YAML/TOML, module files, build files and dot-paths are
   config; everything else is product code. Counts come from
   `git apply --numstat` on the bundled `candidate.diff`.
6. **What to do**: to approve, click **Approve**, choose **Add approval
   note** and paste the approval line; to reject, close the gate with the
   rejection line (with a `bd gate resolve` example). Each line is alone in its
   own code block for copying, with the warning that **Approve** without the
   note is not approval. Then tell Claude, or run the shown
   `factory review-sync` command.
7. **After you approve**: one sentence saying that the shown
   `factory integrate` command then applies the patch to your checkout without
   committing, reruns the checks and closes the task.
8. **Technical details**: everything else.
   - **This request**: task, run ID, base HEAD and candidate fingerprint
     (approval covers this exact candidate only), the statement that approval
     does not commit, push, merge, integrate or close anything, the local-review
     notice and the request marker.
   - **Phases and checks**: one line per phase and check with its stage name,
     exit code, recorded verdict, finding counts and, for checks, the command.
   - **Reported details (untrusted)**: the task description, the full Dev, QA
     and Review reports, every finding with its severity (at most 15 per phase
     are quoted; the rest are counted, with a pointer to `result.json`), and the
     per-file change list.
   - **Evidence**: the terminal `factory review` command, bundle, manifest,
     candidate and index diffs, phase logs and results, and, last, the
     candidate diff itself when it is at most 16,000 bytes.

Sections 1 to 6 fit in about the first 40 lines, including both complete
decision lines. No run ID, HEAD, fingerprint, request marker, local path or
phase name (such as `phase-dev` or `check-01`) appears above **What to do**,
except inside the approval and rejection lines themselves.

The task title, the plain-language summary and the reviewer notes appear above
the decision steps; everything else written by the task author, the agents or
the candidate (task description, reports, findings and file paths) appears only
under **Technical details**. All of it is treated as data:

- It is labelled as written by the AI developer or reviewers, or quoted under
  **Reported details (untrusted)**.
- It is bounded (title 100 bytes, each `explain` field 300 characters, task
  description 1,500 bytes, Dev report 2,500, other reports 1,200, each finding
  400, 50 files, 200 bytes per path); truncation in the reports is stated
  together with the full file to read.
- Above the steps, the title, `explain` fields and reviewer notes are
  flattened to one escaped line each. Further down, prose is shown as a
  blockquote. Either way Markdown special characters are backslash-escaped and
  control/format characters removed, so it cannot open a heading, code block,
  link, image or HTML. File paths are in a fenced block longer than any
  backtick run they contain.
- Strings shaped like decision tokens are rewritten, for example
  `factory-approve(quoted):`, so outside the embedded diff the only copyable
  `factory-approve:`, `factory-reject:` and `factory-review:` strings in a gate
  are the factory's own.

### Dev's plain-language summary

Dev's final outcome may carry an optional `explain` object:

```json
{"version":1,"status":"complete","summary":"...",
 "explain":{"what_changed":"...","why":"...","risk":"..."}}
```

Each field must hold 1 to 300 characters; an empty, overlong or unknown field
fails the Dev phase like any other contract error, and `result.json` is checked
the same way when it is read back. For Claude the Dev role schema requires
`explain`, and the Claude Dev prompt asks for it in plain language for a reader
who is not a developer. Copilot's prompt is unchanged, so its outcome normally
has no `explain`, which the controller accepts. The fields are shown to the
human only; the controller never uses them for any decision.

The embedded candidate diff in **Evidence** stays verbatim inside its fence, so
it can contain token-shaped text. Such text cannot approve anything: a token is
bound to the candidate's fingerprint, which the candidate cannot know about
itself. Copy decision tokens only from **What to do**. The
description is never cut: if it would exceed 64,000 bytes, the embedded diff is
replaced by a pointer to `candidate.diff`, and publication is refused if that
is still too long. The description never decides anything: only the gate's
close reason, checked against the exact tokens, does.

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
**Needs You**, so the decision steps near the top are visible without
scrolling past evidence. New untracked files appear as additions in the change
list and diff, not disappearing as they do in an ordinary `git diff`.

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
