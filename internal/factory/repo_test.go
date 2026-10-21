package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreesDir_SiblingOfRepositoryOutsideGit(t *testing.T) {
	for common, want := range map[string]string{
		"/x/proj/.git": "/x/proj.factory-runs",
		"/x/proj.git":  "/x/proj.git.factory-runs",
	} {
		if got := worktreesDir(common); got != want {
			t.Fatalf("worktreesDir(%q) = %q, want %q", common, got, want)
		}
	}
}

func TestCheckRunWorktreePath_FailsClosedInsideGit(t *testing.T) {
	common := "/x/proj/.git"
	for _, bad := range []string{
		"/x/proj/.git/factory/runs/abc/worktree",
		"/x/proj/.git",
		"/x/proj.factory-runs/.git/abc",
		"/y/.git/abc",
		"relative/abc",
	} {
		if err := checkRunWorktreePath(common, bad); err == nil {
			t.Fatalf("guard accepted %q", bad)
		}
	}
	// A child named "..foo" is inside; only the containment check can catch it here.
	if err := checkRunWorktreePath("/x/proj.git", "/x/proj.git/..foo"); err == nil {
		t.Fatal("guard accepted a child named ..foo")
	}
	if err := checkRunWorktreePath(common, "/x/proj.factory-runs/abc"); err != nil {
		t.Fatalf("guard rejected sibling worktree: %v", err)
	}
}

func TestDiscover_LinkedWorktreeAgreesOnWorktreesDir(t *testing.T) {
	c := setupController(t)
	root := c.Repo.Root
	if want := filepath.Join(filepath.Dir(root), filepath.Base(root)+".factory-runs"); c.Repo.Worktrees != want {
		t.Fatalf("Worktrees = %q, want %q", c.Repo.Worktrees, want)
	}
	linkedPath := filepath.Join(t.TempDir(), "linked")
	if _, err := gitOutput(t.Context(), root, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", linkedPath, "HEAD"); err != nil {
		t.Fatal(err)
	}
	linked, err := Discover(t.Context(), linkedPath)
	if err != nil {
		t.Fatal(err)
	}
	if linked.Root == c.Repo.Root || linked.Common != c.Repo.Common || linked.Worktrees != c.Repo.Worktrees {
		t.Fatalf("linked discovery disagrees: main %+v linked %+v", c.Repo, linked)
	}
}

func TestRun_WorktreeCreatedOutsideCommonGitDir(t *testing.T) {
	c := setupController(t)
	r, err := c.Run(t.Context(), request())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "awaiting_review" {
		t.Fatalf("unexpected state: %+v", r)
	}
	if want := filepath.Join(c.Repo.Worktrees, r.ID); r.Worktree != want {
		t.Fatalf("worktree = %q, want %q", r.Worktree, want)
	}
	if r.Dir != filepath.Join(c.Repo.Common, "factory", "runs", r.ID) {
		t.Fatalf("run dir moved: %q", r.Dir)
	}
	if rel, err := filepath.Rel(c.Repo.Common, r.Worktree); err != nil || !strings.HasPrefix(rel, "..") {
		t.Fatalf("worktree inside common git dir: %q (%v)", rel, err)
	}
	for _, part := range strings.Split(r.Worktree, string(filepath.Separator)) {
		if part == ".git" {
			t.Fatalf("worktree has .git component: %q", r.Worktree)
		}
	}
	info, err := os.Stat(c.Repo.Worktrees)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("worktrees dir not private: %v %v", info, err)
	}
	if fp, err := Fingerprint(t.Context(), r.Worktree); err != nil || fp == "" {
		t.Fatalf("fingerprint on new worktree: %q %v", fp, err)
	}
}

func TestRun_RefusesSymlinkedWorktreesRootBeforeWorktreeAdd(t *testing.T) {
	c := setupController(t)
	target := filepath.Join(c.Repo.Common, "planted")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, c.Repo.Worktrees); err != nil {
		t.Fatal(err)
	}
	r, err := c.Run(t.Context(), request())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("run accepted a symlinked worktrees root: %+v %v", r, err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("worktree created through planted symlink: %v %v", entries, err)
	}
	list, err := gitOutput(t.Context(), c.Repo.Root, "worktree", "list", "--porcelain")
	if err != nil || strings.Contains(list, r.ID) {
		t.Fatalf("git worktree add ran: %q %v", list, err)
	}
}

func TestRun_RefusesLoosePermissionWorktreesRoot(t *testing.T) {
	c := setupController(t)
	if err := os.Mkdir(c.Repo.Worktrees, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.Repo.Worktrees, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := c.Run(t.Context(), request())
	if err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("run accepted a 0755 worktrees root: %+v %v", r, err)
	}
	if _, err := os.Lstat(filepath.Join(c.Repo.Worktrees, r.ID)); !os.IsNotExist(err) {
		t.Fatalf("worktree created under loose root: %v", err)
	}
	if info, err := os.Stat(c.Repo.Worktrees); err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("guard modified the bad root: %v %v", info, err)
	}
}

func TestRun_AcceptsPrivateWorktreesRootFromEarlierRun(t *testing.T) {
	c := setupController(t)
	if err := os.Mkdir(c.Repo.Worktrees, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := c.Run(t.Context(), request())
	if err != nil || r.State != "awaiting_review" {
		t.Fatalf("run refused a private existing root: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(c.Repo.Worktrees, r.ID)); err != nil {
		t.Fatal(err)
	}
}
