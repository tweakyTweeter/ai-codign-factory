package factory

import (
	"os"
	"path/filepath"
	"strings"
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
