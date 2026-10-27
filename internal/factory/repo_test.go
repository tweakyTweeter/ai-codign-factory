package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorktreesDir_SiblingOfMainWorktree(t *testing.T) {
	for mainPath, want := range map[string]string{
		"/x/proj":      "/x/proj.factory-runs",
		"/super/sub":   "/super/sub.factory-runs",
		"/x/store.git": "/x/store.git.factory-runs",
	} {
		if got := worktreesDir(mainPath); got != want {
			t.Fatalf("worktreesDir(%q) = %q, want %q", mainPath, got, want)
		}
	}
}

func TestMainWorktree_ParsesFirstRecordAndRefusesBare(t *testing.T) {
	got, err := mainWorktree("worktree /x/proj\x00HEAD abc\x00branch refs/heads/main\x00\x00worktree /y/linked\x00bare\x00\x00")
	if err != nil || got != "/x/proj" {
		t.Fatalf("mainWorktree = %q, %v; want /x/proj", got, err)
	}
	if _, err := mainWorktree("worktree /x/proj.git\x00bare\x00\x00"); err == nil || !strings.Contains(err.Error(), "bare") {
		t.Fatalf("bare repository accepted: %v", err)
	}
	for _, bad := range []string{"", "HEAD abc\x00", "worktree \x00", "worktree relative\x00"} {
		if got, err := mainWorktree(bad); err == nil {
			t.Fatalf("malformed porcelain %q accepted as %q", bad, got)
		}
	}
}

func TestDiscover_RefusesLinkedWorktreeOfBareRepository(t *testing.T) {
	c := setupController(t)
	bare := filepath.Join(filepath.Dir(c.Repo.Root), "proj.git")
	gitIn(t, filepath.Dir(c.Repo.Root), "clone", "-q", "--bare", c.Repo.Root, bare)
	linked := filepath.Join(filepath.Dir(c.Repo.Root), "linked")
	gitIn(t, bare, "worktree", "add", "-q", "--detach", linked, "HEAD")
	if repo, err := Discover(t.Context(), linked); err == nil || !strings.Contains(err.Error(), "bare") {
		t.Fatalf("Discover accepted a bare repository's worktree: %+v %v", repo, err)
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

// gitIn runs a fixture git command in dir with hooks and system config disabled.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v in %s: %s: %v", args, dir, b, err)
	}
	return strings.TrimSpace(string(b))
}

func TestRun_SubmoduleWorktreesRootBesideCheckoutOutsideGit(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(base, "sub-origin")
	super := filepath.Join(base, "super")
	for _, dir := range []string{sub, super} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "init", "-q", "--initial-branch=main")
	}
	if err := os.WriteFile(filepath.Join(sub, "tracked.txt"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, sub, "add", "tracked.txt")
	gitIn(t, sub, "commit", "-q", "-m", "sub baseline")
	gitIn(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	gitIn(t, super, "commit", "-q", "-m", "add submodule")

	checkout := filepath.Join(super, "sub")
	repo, err := Discover(t.Context(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(super, "sub.factory-runs"); repo.Worktrees != want {
		t.Fatalf("Worktrees = %q, want %q (common %q)", repo.Worktrees, want, repo.Common)
	}
	store, err := Open(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	c := Controller{Repo: repo, Store: store}
	r, err := c.Run(t.Context(), request())
	if err != nil || r.State != "awaiting_review" {
		t.Fatalf("submodule run failed: %+v %v", r, err)
	}
	if want := filepath.Join(repo.Worktrees, r.ID); r.Worktree != want {
		t.Fatalf("worktree = %q, want %q", r.Worktree, want)
	}
	for _, part := range strings.Split(r.Worktree, string(filepath.Separator)) {
		if part == ".git" {
			t.Fatalf("worktree has .git component: %q", r.Worktree)
		}
	}
	if _, err := os.Stat(filepath.Join(r.Worktree, "tracked.txt")); err != nil {
		t.Fatalf("submodule worktree not checked out: %v", err)
	}
	linkedPath := filepath.Join(base, "sub-linked")
	gitIn(t, checkout, "worktree", "add", "-q", "--detach", linkedPath, "HEAD")
	linked, err := Discover(t.Context(), linkedPath)
	if err != nil || linked.Common != repo.Common || linked.Worktrees != repo.Worktrees {
		t.Fatalf("submodule linked worktree disagrees: main %+v linked %+v %v", repo, linked, err)
	}
}
