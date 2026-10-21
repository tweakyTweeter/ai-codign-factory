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
