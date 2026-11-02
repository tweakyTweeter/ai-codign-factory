package factory

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// isolatedHome points HOME at a fresh temp dir and clears CLAUDE_CONFIG_DIR,
// then proves claudeProjectsDir resolves inside it. No test that calls
// PostRun or claudeProjectsDir may ever reach the real home directory.
func isolatedHome(t *testing.T) (home, projects string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	projects = filepath.Join(home, ".claude", "projects")
	got, err := claudeProjectsDir()
	if err != nil || got != projects || !strings.HasPrefix(got, home+string(filepath.Separator)) {
		t.Fatalf("projects dir %q escapes test HOME %q: %v", got, home, err)
	}
	return home, projects
}

func postRunSetup(t *testing.T) (launch, projects string, before map[string]string) {
	t.Helper()
	_, projects = isolatedHome(t)
	launch = filepath.Join(t.TempDir(), "factory-claude-launch-123456")
	if err := os.MkdirAll(filepath.Join(launch, "inputs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launch, "inputs", "prompt.txt"), []byte("p"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := treeManifest(launch)
	if err != nil {
		t.Fatal(err)
	}
	return launch, projects, before
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestClaudePostRun_CleanRunRemovesEmptyResidue(t *testing.T) {
	launch, projects, before := postRunSetup(t)
	residue := filepath.Join(projects, "-private-var-x-factory-claude-launch-123456", "memory")
	other := filepath.Join(projects, "-Users-me-project", "memory")
	mustMkdir(t, residue)
	mustMkdir(t, other)
	out := &PhaseOutcome{}
	if err := (claudeDriver{}).PostRun(launch, before, out); err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(residue)); !os.IsNotExist(err) {
		t.Fatal("empty residue not removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("unrelated project entry touched")
	}
	if out.FailureReason != "" || len(out.Residue) != 0 || out.Runtime != "claude" {
		t.Fatalf("clean run outcome: %+v", out)
	}
}

func TestClaudePostRun_NoProjectsDirIsClean(t *testing.T) {
	launch, _, before := postRunSetup(t)
	if err := (claudeDriver{}).PostRun(launch, before, &PhaseOutcome{}); err != nil {
		t.Fatalf("missing projects dir failed: %v", err)
	}
}

func TestClaudePostRun_FailsClosed(t *testing.T) {
	t.Run("launch litter", func(t *testing.T) {
		launch, _, before := postRunSetup(t)
		if err := os.WriteFile(filepath.Join(launch, "stray.txt"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		out := &PhaseOutcome{}
		if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || out.FailureReason != "isolation_mismatch" {
			t.Fatalf("launch litter accepted: %v", err)
		}
	})
	t.Run("input changed", func(t *testing.T) {
		launch, _, before := postRunSetup(t)
		if err := os.WriteFile(filepath.Join(launch, "inputs", "prompt.txt"), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		out := &PhaseOutcome{}
		if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || out.FailureReason != "isolation_mismatch" {
			t.Fatal("changed launch file accepted")
		}
	})
	t.Run("input removed", func(t *testing.T) {
		launch, _, before := postRunSetup(t)
		if err := os.Remove(filepath.Join(launch, "inputs", "prompt.txt")); err != nil {
			t.Fatal(err)
		}
		out := &PhaseOutcome{}
		if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || out.FailureReason != "isolation_mismatch" {
			t.Fatal("removed launch file accepted")
		}
	})
	t.Run("residue with file", func(t *testing.T) {
		launch, projects, before := postRunSetup(t)
		dir := filepath.Join(projects, "-x-factory-claude-launch-123456", "memory")
		mustMkdir(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("leak"), 0600); err != nil {
			t.Fatal(err)
		}
		out := &PhaseOutcome{}
		if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || len(out.Residue) != 1 || out.FailureReason != "isolation_mismatch" {
			t.Fatalf("global write accepted: %+v: %v", out, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "MEMORY.md")); err != nil {
			t.Fatal("non-empty residue must be kept as evidence")
		}
	})
	t.Run("symlink inside residue", func(t *testing.T) {
		launch, projects, before := postRunSetup(t)
		target := filepath.Join(t.TempDir(), "target")
		mustMkdir(t, target)
		dir := filepath.Join(projects, "-x-factory-claude-launch-123456", "memory")
		mustMkdir(t, dir)
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		out := &PhaseOutcome{}
		if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || len(out.Residue) != 1 || out.FailureReason != "isolation_mismatch" {
			t.Fatalf("symlink residue accepted: %+v: %v", out, err)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatal("symlink residue must be kept as evidence")
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatal("symlink target touched")
		}
	})
	t.Run("residue entry is a symlink", func(t *testing.T) {
		launch, projects, before := postRunSetup(t)
		target := filepath.Join(t.TempDir(), "target")
		mustMkdir(t, filepath.Join(target, "memory"))
		mustMkdir(t, projects)
		link := filepath.Join(projects, "-x-factory-claude-launch-123456")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		out := &PhaseOutcome{}
		if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || len(out.Residue) != 1 || out.FailureReason != "isolation_mismatch" {
			t.Fatalf("symlinked residue entry accepted: %+v: %v", out, err)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Fatal("symlinked residue entry must be kept")
		}
		if _, err := os.Stat(filepath.Join(target, "memory")); err != nil {
			t.Fatal("symlink target followed")
		}
	})
	t.Run("residue entry is a file", func(t *testing.T) {
		launch, projects, before := postRunSetup(t)
		mustMkdir(t, projects)
		f := filepath.Join(projects, "-x-factory-claude-launch-123456")
		if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		out := &PhaseOutcome{}
		if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || len(out.Residue) != 1 || out.FailureReason != "isolation_mismatch" {
			t.Fatalf("file residue entry accepted: %+v: %v", out, err)
		}
		if _, err := os.Stat(f); err != nil {
			t.Fatal("file residue entry must be kept")
		}
	})
}

// A concurrent launch whose random suffix extends ours (MkdirTemp suffixes
// vary in length) must never be touched or blamed on this phase.
func TestClaudePostRun_IgnoresLongerNameContainingToken(t *testing.T) {
	launch, projects, before := postRunSetup(t)
	emptyOther := filepath.Join(projects, "-private-var-x-factory-claude-launch-1234567", "memory")
	fullOther := filepath.Join(projects, "-private-var-x-factory-claude-launch-123456-extra", "memory")
	mustMkdir(t, emptyOther)
	mustMkdir(t, fullOther)
	if err := os.WriteFile(filepath.Join(fullOther, "MEMORY.md"), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	out := &PhaseOutcome{}
	if err := (claudeDriver{}).PostRun(launch, before, out); err != nil || len(out.Residue) != 0 {
		t.Fatalf("unrelated entries blamed: %+v: %v", out, err)
	}
	if _, err := os.Stat(emptyOther); err != nil {
		t.Fatal("other launch's empty residue removed")
	}
	if _, err := os.Stat(filepath.Join(fullOther, "MEMORY.md")); err != nil {
		t.Fatal("other launch's residue touched")
	}
}

func TestRemoveEmptyTree(t *testing.T) {
	isolatedHome(t)
	root := filepath.Join(t.TempDir(), "r")
	mustMkdir(t, filepath.Join(root, "a", "b"))
	mustMkdir(t, filepath.Join(root, "c"))
	if err := removeEmptyTree(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("empty tree kept")
	}
	mustMkdir(t, filepath.Join(root, "a"))
	if err := os.WriteFile(filepath.Join(root, "a", "f"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeEmptyTree(root); err == nil {
		t.Fatal("tree with an (empty) file removed")
	}
	if _, err := os.Stat(filepath.Join(root, "a", "f")); err != nil {
		t.Fatal("file removed")
	}
}

func TestClaudeProjectsDir_HonoursConfigDir(t *testing.T) {
	isolatedHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if d, err := claudeProjectsDir(); err != nil || d != filepath.Join(cfg, "projects") {
		t.Fatalf("got %q: %v", d, err)
	}
}

func TestClaudePostRun_UsesConfigDir(t *testing.T) {
	launch, homeProjects, before := postRunSetup(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	inCfg := filepath.Join(cfg, "projects", "-x-factory-claude-launch-123456", "memory")
	inHome := filepath.Join(homeProjects, "-x-factory-claude-launch-123456", "memory")
	mustMkdir(t, inCfg)
	mustMkdir(t, inHome)
	if err := (claudeDriver{}).PostRun(launch, before, &PhaseOutcome{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(inCfg)); !os.IsNotExist(err) {
		t.Fatal("config-dir residue not removed")
	}
	if _, err := os.Stat(inHome); err != nil {
		t.Fatal("HOME projects touched although CLAUDE_CONFIG_DIR is set")
	}
}

func TestClaudeProjectsDir_RejectsRelativeConfigDir(t *testing.T) {
	isolatedHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "rel/cfg")
	if d, err := claudeProjectsDir(); err == nil {
		t.Fatalf("relative config dir accepted: %q", d)
	}
}

// removeDirOnly must never unlink a non-directory, even if one was swapped in
// after the Lstat check.
func TestRemoveDirOnly_RefusesNonDirectories(t *testing.T) {
	isolatedHome(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeDirOnly(file); err == nil {
		t.Fatal("regular file removed")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("regular file gone")
	}
	target := filepath.Join(dir, "target")
	mustMkdir(t, target)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := removeDirOnly(link); err == nil {
		t.Fatal("symlink removed")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("symlink gone")
	}
	if err := removeDirOnly(target); err != nil {
		t.Fatalf("empty dir not removed: %v", err)
	}
}

func TestCheckLaunchPathLength(t *testing.T) {
	short := "/tmp/factory-claude-launch-123456"
	if err := checkLaunchPathLength(short); err != nil {
		t.Fatalf("short path rejected: %v", err)
	}
	long := "/" + strings.Repeat("a", maxSanitisedProjectName) + "/factory-claude-launch-123456"
	if err := checkLaunchPathLength(long); err == nil {
		t.Fatal("over-long launch path accepted")
	}
	exact := "/" + strings.Repeat("a", maxSanitisedProjectName-1)
	if err := checkLaunchPathLength(exact); err != nil {
		t.Fatalf("path at the limit rejected: %v", err)
	}
}

func TestClaudePostRun_FailsClosedOnLongLaunchPath(t *testing.T) {
	isolatedHome(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("a", maxSanitisedProjectName), "factory-claude-launch-123456")
	mustMkdir(t, long)
	before, err := treeManifest(long)
	if err != nil {
		t.Fatal(err)
	}
	out := &PhaseOutcome{}
	err = (claudeDriver{}).PostRun(long, before, out)
	if err == nil || out.FailureReason != "isolation_mismatch" || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("long launch path accepted: %+v: %v", out, err)
	}
}

const testSession = "95075eb9-17eb-4d68-99cf-5049e6204250"

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudePostRun_CleansToolResultSpills(t *testing.T) {
	launch, projects, before := postRunSetup(t)
	entry := filepath.Join(projects, sanitiseProjectName(launch))
	mustMkdir(t, filepath.Join(entry, "memory"))
	mustWrite(t, filepath.Join(entry, testSession, "tool-results", "toolu_b.txt"), "bbbbbbb")
	mustWrite(t, filepath.Join(entry, testSession, "tool-results", "toolu_a.txt"), "aaa")
	other := filepath.Join(projects, "-Users-me-project", testSession, "tool-results", "keep.txt")
	mustWrite(t, other, "other")
	out := &PhaseOutcome{}
	if err := (claudeDriver{}).PostRun(launch, before, out); err != nil {
		t.Fatalf("spill run failed: %v", err)
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatalf("spill entry not removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("unrelated project entry touched")
	}
	want := []SpillFile{{Path: filepath.Join(testSession, "tool-results", "toolu_a.txt"), Size: 3}, {Path: filepath.Join(testSession, "tool-results", "toolu_b.txt"), Size: 7}}
	if out.FailureReason != "" || len(out.Residue) != 0 || !slices.Equal(out.SpillFiles, want) {
		t.Fatalf("spill outcome: %+v", out)
	}
}

func TestClaudePostRun_SpillWithAnythingElseFailsClosed(t *testing.T) {
	spill := filepath.Join(testSession, "tool-results", "toolu_a.txt")
	for name, plant := range map[string]func(t *testing.T, entry string){
		"memory file":         func(t *testing.T, e string) { mustWrite(t, filepath.Join(e, "memory", "MEMORY.md"), "leak") },
		"file in session dir": func(t *testing.T, e string) { mustWrite(t, filepath.Join(e, testSession, "x.jsonl"), "x") },
		"file at entry root":  func(t *testing.T, e string) { mustWrite(t, filepath.Join(e, "x.txt"), "x") },
		"non-uuid session": func(t *testing.T, e string) {
			mustWrite(t, filepath.Join(e, "not-a-session", "tool-results", "x.txt"), "x")
		},
		"uppercase tool-results": func(t *testing.T, e string) {
			// A separate session: macOS folds case, so under testSession this would land in tool-results.
			mustWrite(t, filepath.Join(e, "0d8c7e9a-1b2c-4d5e-8f90-a1b2c3d4e5f6", "Tool-Results", "x.txt"), "x")
		},
		"nested in tool-results": func(t *testing.T, e string) {
			mustWrite(t, filepath.Join(e, testSession, "tool-results", "sub", "x.txt"), "x")
		},
		"symlink in tool-results": func(t *testing.T, e string) {
			target := filepath.Join(t.TempDir(), "target.txt")
			mustWrite(t, target, "secret")
			if err := os.Symlink(target, filepath.Join(e, testSession, "tool-results", "link.txt")); err != nil {
				t.Fatal(err)
			}
		},
		"fifo in tool-results": func(t *testing.T, e string) {
			if err := syscall.Mkfifo(filepath.Join(e, testSession, "tool-results", "fifo"), 0600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			launch, projects, before := postRunSetup(t)
			entry := filepath.Join(projects, sanitiseProjectName(launch))
			mustWrite(t, filepath.Join(entry, spill), "spilled")
			plant(t, entry)
			want, err := treeManifest(entry)
			if err != nil {
				t.Fatal(err)
			}
			out := &PhaseOutcome{}
			err = (claudeDriver{}).PostRun(launch, before, out)
			if err == nil || out.FailureReason != "isolation_mismatch" || !slices.Equal(out.Residue, []string{entry}) || !strings.Contains(err.Error(), "Claude wrote global state") {
				t.Fatalf("%s accepted: %+v: %v", name, out, err)
			}
			if len(out.SpillFiles) != 0 {
				t.Fatalf("spill recorded as cleaned although kept: %+v", out.SpillFiles)
			}
			got, err := treeManifest(entry)
			if err != nil || !maps.Equal(got, want) {
				t.Fatalf("evidence changed: %v\nwant %v\ngot  %v", err, want, got)
			}
		})
	}
}

func TestClaudePostRun_SymlinkedToolResultsDirFailsClosed(t *testing.T) {
	launch, projects, before := postRunSetup(t)
	entry := filepath.Join(projects, sanitiseProjectName(launch))
	target := filepath.Join(t.TempDir(), "elsewhere")
	mustWrite(t, filepath.Join(target, "victim.txt"), "keep me")
	mustMkdir(t, filepath.Join(entry, testSession))
	if err := os.Symlink(target, filepath.Join(entry, testSession, "tool-results")); err != nil {
		t.Fatal(err)
	}
	out := &PhaseOutcome{}
	if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || len(out.Residue) != 1 {
		t.Fatalf("symlinked tool-results accepted: %+v: %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(target, "victim.txt")); err != nil {
		t.Fatal("symlink target followed and deleted")
	}
}

func TestIsSessionID(t *testing.T) {
	for s, want := range map[string]bool{
		testSession:                             true,
		"95075EB9-17EB-4D68-99CF-5049E6204250":  false, // Claude writes lowercase
		"95075eb9-17eb-4d68-99cf-5049E6204250":  false,
		"memory":                                false,
		"95075eb9-17eb-4d68-99cf-5049e620425":   false,
		"95075eb9-17eb-4d68-99cf-5049e62042500": false,
		"95075eb9x17eb-4d68-99cf-5049e6204250":  false,
		"g5075eb9-17eb-4d68-99cf-5049e6204250":  false,
		"":                                      false,
	} {
		if isSessionID(s) != want {
			t.Errorf("isSessionID(%q) = %v", s, !want)
		}
	}
}

func TestClaudeReserveSpillDir(t *testing.T) {
	_, projects := isolatedHome(t)
	launch := filepath.Join(resolvedTempDir(t), "factory-claude-launch-42")
	mustMkdir(t, launch)
	dir, release, err := (claudeDriver{}).ReserveSpillDir(launch)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(projects, sanitiseProjectName(launch)) {
		t.Fatalf("reserved %q", dir)
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0700 {
		t.Fatalf("reserved entry: %v: %v", fi, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("release did not remove the empty entry")
	}
	if err := release(); err != nil {
		t.Fatalf("release of an already removed entry: %v", err)
	}
}

func TestClaudeReserveSpillDir_ReleaseKeepsContent(t *testing.T) {
	launch := filepath.Join(resolvedTempDir(t), "factory-claude-launch-42")
	mustMkdir(t, launch)
	_, projects := isolatedHome(t)
	dir, release, err := (claudeDriver{}).ReserveSpillDir(launch)
	if err != nil || !strings.HasPrefix(dir, projects+string(filepath.Separator)) {
		t.Fatalf("reserved %q outside %q: %v", dir, projects, err)
	}
	mustWrite(t, filepath.Join(dir, "memory", "MEMORY.md"), "x")
	if err := release(); err == nil {
		t.Fatal("release of a non-empty entry succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "memory", "MEMORY.md")); err != nil {
		t.Fatal("release removed content")
	}
}

func TestClaudeReserveSpillDir_FailsClosedOnExistingEntry(t *testing.T) {
	_, projects := isolatedHome(t)
	launch := filepath.Join(resolvedTempDir(t), "factory-claude-launch-42")
	mustMkdir(t, launch)
	stale := filepath.Join(projects, sanitiseProjectName(launch))
	mustWrite(t, filepath.Join(stale, "memory", "MEMORY.md"), "stale")
	if _, _, err := (claudeDriver{}).ReserveSpillDir(launch); err == nil {
		t.Fatal("existing entry reused")
	}
	if _, err := os.Stat(filepath.Join(stale, "memory", "MEMORY.md")); err != nil {
		t.Fatal("stale entry touched")
	}
	link := filepath.Join(resolvedTempDir(t), "factory-claude-launch-43")
	mustMkdir(t, link)
	if err := os.Symlink(t.TempDir(), filepath.Join(projects, sanitiseProjectName(link))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (claudeDriver{}).ReserveSpillDir(link); err == nil {
		t.Fatal("symlinked entry reused")
	}
}

func TestClaudeReserveSpillDir_FailsClosedOnLongLaunchPath(t *testing.T) {
	isolatedHome(t)
	long := filepath.Join(resolvedTempDir(t), strings.Repeat("a", maxSanitisedProjectName), "factory-claude-launch-1")
	mustMkdir(t, long)
	if _, _, err := (claudeDriver{}).ReserveSpillDir(long); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("long launch path reserved: %v", err)
	}
}

func TestCopilotReserveSpillDir_IsNoop(t *testing.T) {
	_, projects := isolatedHome(t)
	dir, release, err := (copilotDriver{}).ReserveSpillDir(t.TempDir())
	if err != nil || dir != "" || release() != nil {
		t.Fatalf("copilot reserved %q: %v", dir, err)
	}
	if _, err := os.Lstat(projects); !os.IsNotExist(err) {
		t.Fatal("copilot touched the Claude projects dir")
	}
}

// Only the exact entry ReserveSpillDir created may have spills deleted. An
// entry that merely ends with this launch's name (another TMPDIR, a kept
// evidence entry, or a mispredicted name) keeps the empty-tree-only rule.
func TestClaudePostRun_SpillInMispredictedEntryFailsClosed(t *testing.T) {
	launch, projects, before := postRunSetup(t)
	entry := filepath.Join(projects, "-elsewhere-factory-claude-launch-123456")
	if entry == filepath.Join(projects, sanitiseProjectName(launch)) {
		t.Fatal("test entry must differ from the reserved name")
	}
	mustWrite(t, filepath.Join(entry, testSession, "tool-results", "toolu_a.txt"), "spilled")
	want, err := treeManifest(entry)
	if err != nil {
		t.Fatal(err)
	}
	out := &PhaseOutcome{}
	err = (claudeDriver{}).PostRun(launch, before, out)
	if err == nil || out.FailureReason != "isolation_mismatch" || !slices.Equal(out.Residue, []string{entry}) || len(out.SpillFiles) != 0 {
		t.Fatalf("spill in a non-reserved entry cleaned: %+v: %v", out, err)
	}
	if got, err := treeManifest(entry); err != nil || !maps.Equal(got, want) {
		t.Fatalf("non-reserved entry changed: %v", err)
	}
}

// The reserved entry itself replaced by a symlink: the audit must refuse it
// and never delete anything in the symlink's target.
func TestClaudePostRun_SymlinkedEntryFailsClosed(t *testing.T) {
	launch, projects, before := postRunSetup(t)
	target := filepath.Join(t.TempDir(), "target")
	victim := filepath.Join(target, testSession, "tool-results", "toolu_a.txt")
	mustWrite(t, victim, "keep me")
	mustMkdir(t, projects)
	entry := filepath.Join(projects, sanitiseProjectName(launch))
	if err := os.Symlink(target, entry); err != nil {
		t.Fatal(err)
	}
	out := &PhaseOutcome{}
	if err := (claudeDriver{}).PostRun(launch, before, out); err == nil || out.FailureReason != "isolation_mismatch" || len(out.SpillFiles) != 0 {
		t.Fatalf("symlinked entry accepted: %+v: %v", out, err)
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep me" {
		t.Fatalf("symlink target touched: %v", err)
	}
	if fi, err := os.Lstat(entry); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink evidence removed: %v", err)
	}
}

// cleanProjectEntry opens the entry once and must refuse a path that is not
// itself a real directory, even if it resolves to one.
func TestCleanProjectEntry_RefusesSymlinkedEntry(t *testing.T) {
	isolatedHome(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	victim := filepath.Join(target, testSession, "tool-results", "toolu_a.txt")
	mustWrite(t, victim, "keep me")
	link := filepath.Join(dir, "entry")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if spills, err := cleanProjectEntry(link); err == nil || len(spills) != 0 {
		t.Fatalf("symlinked entry cleaned: %v: %v", spills, err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("symlink target deleted")
	}
}

// Removing a hard-linked spill unlinks only the entry's name.
func TestClaudePostRun_HardLinkedSpillKeepsOtherLink(t *testing.T) {
	launch, projects, before := postRunSetup(t)
	entry := filepath.Join(projects, sanitiseProjectName(launch))
	other := filepath.Join(projects, "..", "hardlink-other.txt")
	mustWrite(t, other, "shared data")
	spill := filepath.Join(entry, testSession, "tool-results", "toolu_a.txt")
	mustMkdir(t, filepath.Dir(spill))
	if err := os.Link(other, spill); err != nil {
		t.Fatal(err)
	}
	out := &PhaseOutcome{}
	if err := (claudeDriver{}).PostRun(launch, before, out); err != nil {
		t.Fatalf("hard-linked spill run failed: %+v: %v", out, err)
	}
	if b, err := os.ReadFile(other); err != nil || string(b) != "shared data" {
		t.Fatalf("other link damaged: %q: %v", b, err)
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatalf("entry not removed: %v", err)
	}
	want := []SpillFile{{Path: filepath.Join(testSession, "tool-results", "toolu_a.txt"), Size: int64(len("shared data"))}}
	if !slices.Equal(out.SpillFiles, want) {
		t.Fatalf("spills: %+v", out.SpillFiles)
	}
}

func TestClaudePostRun_ZeroByteSpillIsRemoved(t *testing.T) {
	launch, projects, before := postRunSetup(t)
	entry := filepath.Join(projects, sanitiseProjectName(launch))
	mustWrite(t, filepath.Join(entry, testSession, "tool-results", "toolu_empty.txt"), "")
	out := &PhaseOutcome{}
	if err := (claudeDriver{}).PostRun(launch, before, out); err != nil {
		t.Fatalf("zero-byte spill run failed: %+v: %v", out, err)
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatalf("entry not removed: %v", err)
	}
	want := []SpillFile{{Path: filepath.Join(testSession, "tool-results", "toolu_empty.txt"), Size: 0}}
	if !slices.Equal(out.SpillFiles, want) {
		t.Fatalf("spills: %+v", out.SpillFiles)
	}
}

// An entry that already exists when the run starts is stale or foreign: the
// run fails closed and names it as residue, leaving it alone.
func TestReserveSpill_ExistingEntryIsResidue(t *testing.T) {
	_, projects := isolatedHome(t)
	launch := filepath.Join(resolvedTempDir(t), "factory-claude-launch-42")
	mustMkdir(t, launch)
	stale := filepath.Join(projects, sanitiseProjectName(launch))
	mustWrite(t, filepath.Join(stale, "memory", "MEMORY.md"), "stale")
	out := &PhaseOutcome{Runtime: "claude"}
	if _, _, err := reserveSpill(claudeDriver{}, launch, out); err == nil {
		t.Fatal("existing entry reused")
	}
	if out.FailureReason != "isolation_mismatch" || !slices.Equal(out.Residue, []string{stale}) {
		t.Fatalf("outcome: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(stale, "memory", "MEMORY.md")); err != nil {
		t.Fatal("stale entry touched")
	}
}

// A reserved entry that is not empty when an early-failing run releases it
// is global state: isolation_mismatch with the entry kept as residue.
func TestReleaseSpill_NonEmptyEntryIsResidue(t *testing.T) {
	_, projects := isolatedHome(t)
	launch := filepath.Join(resolvedTempDir(t), "factory-claude-launch-42")
	mustMkdir(t, launch)
	out := &PhaseOutcome{Runtime: "claude"}
	dir, release, err := reserveSpill(claudeDriver{}, launch, out)
	if err != nil || !strings.HasPrefix(dir, projects+string(filepath.Separator)) {
		t.Fatalf("reserved %q outside %q: %v", dir, projects, err)
	}
	mustWrite(t, filepath.Join(dir, "memory", "MEMORY.md"), "x")
	out.FailureReason = "policy"
	err = releaseSpill(dir, release, out, errors.New("failure_reason=policy: early"))
	if err == nil || !strings.HasPrefix(err.Error(), "failure_reason=isolation_mismatch") || !strings.Contains(err.Error(), "early") {
		t.Fatalf("release error: %v", err)
	}
	if out.FailureReason != "isolation_mismatch" || !slices.Equal(out.Residue, []string{dir}) {
		t.Fatalf("outcome: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "memory", "MEMORY.md")); err != nil {
		t.Fatal("release removed content")
	}
}

func TestReleaseSpill_EmptyEntryKeepsOutcome(t *testing.T) {
	isolatedHome(t)
	launch := filepath.Join(resolvedTempDir(t), "factory-claude-launch-42")
	mustMkdir(t, launch)
	out := &PhaseOutcome{Runtime: "claude"}
	dir, release, err := reserveSpill(claudeDriver{}, launch, out)
	if err != nil {
		t.Fatal(err)
	}
	out.FailureReason = "policy"
	early := errors.New("failure_reason=policy: early")
	if err := releaseSpill(dir, release, out, early); err != early || out.FailureReason != "policy" || len(out.Residue) != 0 {
		t.Fatalf("clean release changed outcome: %+v: %v", out, err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("empty reserved entry left behind")
	}
}
