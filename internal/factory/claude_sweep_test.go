package factory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const sweepSession = "0123abcd-4567-89ab-cdef-0123456789ab"

// claudeLaunchKilledFixture is the adapter's setup up to the runtime launch:
// a private launch dir holding sealed inputs and a reserved projects entry
// with one tool-result spill. Its cleanup defers never run, because the
// parent SIGKILLs it, exactly like a controller timeout kill.
func claudeLaunchKilledFixture() int {
	launch, err := privateLaunchDir(claudeLaunchPrefix)
	if err != nil {
		return 70
	}
	defer os.RemoveAll(launch) // never runs: the process is SIGKILLed
	phase := filepath.Join(launch, "inputs", "outputs", "dev")
	if err := os.MkdirAll(phase, 0700); err != nil {
		return 71
	}
	ctx := fmt.Sprintf(`{"run":{"id":%q}}`, os.Getenv("FACTORY_SWEEP_RUN_ID"))
	if err := os.WriteFile(filepath.Join(phase, "context.json"), []byte(ctx), 0600); err != nil {
		return 72
	}
	spill, release, err := (claudeDriver{}).ReserveSpillDir(launch)
	if err != nil {
		return 73
	}
	defer release() // never runs either
	tools := filepath.Join(spill, sweepSession, "tool-results")
	if err := os.MkdirAll(tools, 0700); err != nil {
		return 74
	}
	if err := os.WriteFile(filepath.Join(tools, "out.txt"), []byte("spilled"), 0600); err != nil {
		return 75
	}
	fmt.Println(launch)
	<-time.After(time.Hour)
	return 0
}

// sweepEnv isolates HOME/CLAUDE_CONFIG_DIR and TMPDIR so the sweep can only
// ever see test directories. It returns the symlink-resolved TMPDIR.
func sweepEnv(t *testing.T) (tmp, projects string) {
	t.Helper()
	_, projects = isolatedHome(t)
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	tmp, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := filepath.EvalSymlinks(os.TempDir()); got != tmp {
		t.Fatalf("TMPDIR %q not isolated (%q)", got, tmp)
	}
	return tmp, projects
}

// fakeLaunch creates a launch dir as privateInputs leaves it.
func fakeLaunch(t *testing.T, tmp, suffix, runID string) string {
	t.Helper()
	launch := filepath.Join(tmp, claudeLaunchPrefix+suffix)
	phase := filepath.Join(launch, "inputs", "outputs", "dev")
	mustMkdir(t, phase)
	if err := os.WriteFile(filepath.Join(phase, "context.json"), []byte(fmt.Sprintf(`{"run":{"id":%q}}`, runID)), 0600); err != nil {
		t.Fatal(err)
	}
	return launch
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s must be kept: %v", path, err)
	}
}

func mustBeGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s must be removed: %v", path, err)
	}
}

func residueFor(t *testing.T, rs []LaunchResidue, path string) LaunchResidue {
	t.Helper()
	i := slices.IndexFunc(rs, func(r LaunchResidue) bool { return r.Path == path })
	if i < 0 {
		t.Fatalf("no residue recorded for %s in %+v", path, rs)
	}
	return rs[i]
}

func saveRunInState(t *testing.T, c Controller, state string) Run {
	t.Helper()
	id, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	r := Run{ID: id, Dir: c.Repo.Root, Worktree: c.Repo.Root, Evidence: []Evidence{}}
	if err := c.Store.Save(t.Context(), &r, state, "test run", true); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSweep_SIGKILLedAdapterLeftoversRecordedByReconcile(t *testing.T) {
	c := setupController(t)
	tmp, projects := sweepEnv(t)
	r := saveRunInState(t, c, "failed")

	cmd := exec.Command(os.Args[0], "-test.run=^TestFixture$", "--", "claude-launch-killed")
	cmd.Env = append(os.Environ(), "FACTORY_SWEEP_RUN_ID="+r.ID)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("fixture did not report its launch dir: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("fixture was not SIGKILLed: %v", err)
	}
	launch := strings.TrimSpace(line)
	if filepath.Dir(launch) != tmp {
		t.Fatalf("launch %q outside isolated TMPDIR %q", launch, tmp)
	}
	entry := filepath.Join(projects, sanitiseProjectName(launch))
	spill := filepath.Join(entry, sweepSession, "tool-results", "out.txt")
	// The kill skipped both cleanup defers: these are the acf-72 leftovers.
	mustExist(t, launch)
	mustExist(t, spill)

	// Neighbours the sweep must never touch.
	foreignEntry := filepath.Join(projects, sanitiseProjectName(launch)+"7", "memory")
	mustMkdir(t, foreignEntry)
	userEntry := filepath.Join(projects, "-Users-me-project", "memory")
	mustMkdir(t, userEntry)
	other := fakeLaunch(t, tmp, "1", "not-a-run-in-this-journal")
	otherEntry := filepath.Join(projects, sanitiseProjectName(other), "memory")
	mustMkdir(t, otherEntry)
	plain := filepath.Join(tmp, "unrelated-dir", "keep.txt")
	writeFile(t, plain, "x")

	runs, unowned, err := c.ReconcileSweep(t.Context())
	if err != nil || len(runs) != 1 {
		t.Fatalf("reconcile %+v: %v", runs, err)
	}
	got, err := c.Store.Get(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failed" {
		t.Fatalf("sweep changed run state to %s", got.State)
	}
	e := residueFor(t, got.LaunchResidue, entry)
	if !e.Removed || len(e.SpillFiles) != 1 || e.SpillFiles[0].Path != filepath.Join(sweepSession, "tool-results", "out.txt") || e.SpillFiles[0].Size != int64(len("spilled")) {
		t.Fatalf("projects entry residue %+v", e)
	}
	l := residueFor(t, got.LaunchResidue, launch)
	if l.Removed || !strings.Contains(l.Detail, "not empty") {
		t.Fatalf("launch dir residue %+v", l)
	}
	if len(got.LaunchResidue) != 2 {
		t.Fatalf("unexpected residue %+v", got.LaunchResidue)
	}
	mustBeGone(t, entry)
	mustExist(t, filepath.Join(launch, "inputs", "outputs", "dev", "context.json"))
	mustExist(t, foreignEntry)
	mustExist(t, userEntry)
	mustExist(t, otherEntry)
	mustExist(t, plain)
	if len(unowned) != 1 || unowned[0].Path != other || !strings.Contains(unowned[0].Reason, "not in this journal") {
		t.Fatalf("unowned %+v", unowned)
	}
	data, err := json.Marshal(got)
	if err != nil || strings.Contains(string(data), "spilled") {
		t.Fatalf("spill content leaked into the journal: %s: %v", data, err)
	}
	events, err := c.Store.Events(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		n := 0
		for _, ev := range events {
			if ev.State == "launch_residue" {
				n++
			}
		}
		return n
	}
	if count() != 1 {
		t.Fatalf("events %+v", events)
	}
	// A second reconcile finds the same kept launch dir and journals nothing new.
	if _, _, err := c.ReconcileSweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if events, err = c.Store.Events(t.Context(), r.ID); err != nil || count() != 1 {
		t.Fatalf("repeated sweep rejournalled: %+v: %v", events, err)
	}
	again, err := c.Store.Get(t.Context(), r.ID)
	if err != nil || len(again.LaunchResidue) != 2 || len(residueFor(t, again.LaunchResidue, entry).SpillFiles) != 1 {
		t.Fatalf("repeated sweep lost residue %+v: %v", again.LaunchResidue, err)
	}
}

func TestSweep_PossibleOrphanRecordsOnlyUntilAcknowledgedRelease(t *testing.T) {
	c := setupController(t)
	tmp, projects := sweepEnv(t)
	r := saveRunInState(t, c, "worker_running") // reconcile marks it needs_attention
	launch := fakeLaunch(t, tmp, "424242", r.ID)
	entry := filepath.Join(projects, sanitiseProjectName(launch))
	mustMkdir(t, filepath.Join(entry, "memory"))

	if _, err := c.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := c.Store.Get(t.Context(), r.ID)
	if err != nil || got.State != "needs_attention" {
		t.Fatalf("run %+v: %v", got, err)
	}
	if e := residueFor(t, got.LaunchResidue, entry); e.Removed || !strings.Contains(e.Detail, "live descendants") {
		t.Fatalf("possible orphan's entry %+v", e)
	}
	mustExist(t, filepath.Join(entry, "memory"))

	if _, err := c.Release(t.Context(), r.ID, "descendants verified stopped", true); err != nil {
		t.Fatal(err)
	}
	got, err = c.Store.Get(t.Context(), r.ID)
	if err != nil || got.State != "released" {
		t.Fatalf("run %+v: %v", got, err)
	}
	if e := residueFor(t, got.LaunchResidue, entry); !e.Removed || e.Detail != "" {
		t.Fatalf("released run's entry %+v", e)
	}
	mustBeGone(t, entry)
	if l := residueFor(t, got.LaunchResidue, launch); l.Removed {
		t.Fatalf("launch dir with inputs removed: %+v", l)
	}
	mustExist(t, launch)
}

func TestSweep_FailsClosed(t *testing.T) {
	const id = "run-1"
	clean := func(owner string) sweepMode {
		if owner == id {
			return sweepClean
		}
		return sweepUnowned
	}
	t.Run("only spills and empty dirs are removed", func(t *testing.T) {
		tmp, projects := sweepEnv(t)
		launch := fakeLaunch(t, tmp, "100", id)
		token := sanitiseProjectName(filepath.Base(launch))
		reserved := filepath.Join(projects, sanitiseProjectName(launch))
		writeFile(t, filepath.Join(reserved, sweepSession, "tool-results", "a.txt"), "a")
		elsewhere := filepath.Join(projects, "-other-tmp-"+token)
		writeFile(t, filepath.Join(elsewhere, sweepSession, "tool-results", "b.txt"), "b") // spills only allowed in the reserved entry
		empty := filepath.Join(projects, "-third-tmp-"+token)
		mustMkdir(t, filepath.Join(empty, "memory"))
		longer := filepath.Join(projects, sanitiseProjectName(launch)+"0")
		mustMkdir(t, longer)

		found, unowned, err := sweepClaudeLaunches(clean)
		if err != nil || len(unowned) != 0 {
			t.Fatalf("sweep: %+v %v", unowned, err)
		}
		rs := found[id]
		if r := residueFor(t, rs, reserved); !r.Removed || len(r.SpillFiles) != 1 {
			t.Fatalf("reserved %+v", r)
		}
		if r := residueFor(t, rs, elsewhere); r.Removed || !strings.Contains(r.Detail, "not empty") {
			t.Fatalf("non-reserved entry with a spill %+v", r)
		}
		if r := residueFor(t, rs, empty); !r.Removed {
			t.Fatalf("empty entry %+v", r)
		}
		if r := residueFor(t, rs, launch); r.Removed || !strings.Contains(r.Detail, "owner of a kept projects entry") {
			t.Fatalf("launch dir %+v", r)
		}
		mustBeGone(t, reserved)
		mustBeGone(t, empty)
		mustExist(t, filepath.Join(elsewhere, sweepSession, "tool-results", "b.txt"))
		mustExist(t, longer)
		mustExist(t, launch)
	})
	t.Run("non-spill file and symlinks are kept", func(t *testing.T) {
		tmp, projects := sweepEnv(t)
		launch := fakeLaunch(t, tmp, "200", id)
		reserved := filepath.Join(projects, sanitiseProjectName(launch))
		writeFile(t, filepath.Join(reserved, "memory", "MEMORY.md"), "leak")
		target := filepath.Join(t.TempDir(), "target")
		mustMkdir(t, filepath.Join(target, "empty"))
		linked := filepath.Join(projects, "-linked-"+sanitiseProjectName(filepath.Base(launch)))
		if err := os.Symlink(target, linked); err != nil {
			t.Fatal(err)
		}
		found, _, err := sweepClaudeLaunches(clean)
		if err != nil {
			t.Fatal(err)
		}
		if r := residueFor(t, found[id], reserved); r.Removed {
			t.Fatalf("entry with a non-spill file removed %+v", r)
		}
		if r := residueFor(t, found[id], linked); r.Removed {
			t.Fatalf("symlinked entry removed %+v", r)
		}
		mustExist(t, filepath.Join(reserved, "memory", "MEMORY.md"))
		mustExist(t, linked)
		mustExist(t, filepath.Join(target, "empty"))
		mustExist(t, launch)
	})
	t.Run("unidentifiable launch dirs are reported and untouched", func(t *testing.T) {
		tmp, projects := sweepEnv(t)
		outside := fakeLaunch(t, t.TempDir(), "1", id)
		cases := map[string]func(string){
			"symlinked launch dir": func(p string) {
				if err := os.Symlink(outside, p); err != nil {
					t.Fatal(err)
				}
			},
			"symlinked inputs": func(p string) {
				mustMkdir(t, p)
				if err := os.Symlink(filepath.Join(outside, "inputs"), filepath.Join(p, "inputs")); err != nil {
					t.Fatal(err)
				}
			},
			"symlinked context": func(p string) {
				mustMkdir(t, filepath.Join(p, "inputs", "outputs", "dev"))
				if err := os.Symlink(filepath.Join(outside, "inputs", "outputs", "dev", "context.json"), filepath.Join(p, "inputs", "outputs", "dev", "context.json")); err != nil {
					t.Fatal(err)
				}
			},
			"no context": func(p string) {
				mustMkdir(t, filepath.Join(p, "inputs", "outputs", "dev"))
			},
			"conflicting contexts": func(p string) {
				writeFile(t, filepath.Join(p, "inputs", "outputs", "a", "context.json"), `{"run":{"id":"run-1"}}`)
				writeFile(t, filepath.Join(p, "inputs", "outputs", "b", "context.json"), `{"run":{"id":"run-2"}}`)
			},
			"empty dir": func(p string) {
				mustMkdir(t, p)
			},
		}
		i := 300
		var paths []string
		for _, setup := range cases {
			p := filepath.Join(tmp, fmt.Sprintf("%s%d", claudeLaunchPrefix, i))
			i++
			setup(p)
			paths = append(paths, p)
			// An empty projects entry named after it must survive too.
			mustMkdir(t, filepath.Join(projects, sanitiseProjectName(p)))
		}
		found, unowned, err := sweepClaudeLaunches(clean)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 0 || len(unowned) != len(cases) {
			t.Fatalf("found %+v unowned %+v", found, unowned)
		}
		for _, p := range paths {
			mustExist(t, p)
			mustExist(t, filepath.Join(projects, sanitiseProjectName(p)))
		}
		mustExist(t, filepath.Join(outside, "inputs", "outputs", "dev", "context.json"))
	})
	t.Run("skipped runs are silent and unread projects stay unread", func(t *testing.T) {
		tmp, _ := sweepEnv(t)
		launch := fakeLaunch(t, tmp, "400", id)
		// A relative CLAUDE_CONFIG_DIR makes claudeProjectsDir fail: proof
		// that a sweep with nothing to handle never consults it.
		t.Setenv("CLAUDE_CONFIG_DIR", "relative")
		found, unowned, err := sweepClaudeLaunches(func(string) sweepMode { return sweepSkip })
		if err != nil || len(found) != 0 || len(unowned) != 0 {
			t.Fatalf("found %+v unowned %+v: %v", found, unowned, err)
		}
		found, _, err = sweepClaudeLaunches(clean)
		if err != nil {
			t.Fatal(err)
		}
		if r := residueFor(t, found[id], launch); r.Removed || !strings.Contains(r.Detail, "cannot audit") {
			t.Fatalf("launch with unreadable projects dir %+v", r)
		}
		mustExist(t, launch)
	})
}

func TestIsClaudeLaunchName(t *testing.T) {
	for _, name := range []string{"factory-claude-launch-4105976273", "factory-claude-launch-1"} {
		if !isClaudeLaunchName(name) {
			t.Errorf("%q rejected", name)
		}
	}
	for _, name := range []string{
		"factory-claude-launch-", "factory-claude-launch-12a", "factory-claude-launch-1/2",
		"factory-copilot-launch-123", "factory-claude-doctor-123", "xfactory-claude-launch-123",
		"factory-claude-launch-12345678901234567890123",
	} {
		if isClaudeLaunchName(name) {
			t.Errorf("%q accepted", name)
		}
	}
}
