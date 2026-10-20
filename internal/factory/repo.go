package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Repo struct {
	Root   string `json:"root"`
	Common string `json:"common_git_dir"`
	State  string `json:"state_dir"`
}

func Discover(ctx context.Context, path string) (Repo, error) {
	root, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repo{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Repo{}, err
	}
	common, err := gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Repo{}, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return Repo{}, err
	}
	return Repo{Root: root, Common: common, State: filepath.Join(common, "factory")}, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := capture(child, dir, append([]string{"git", "--no-pager"}, args...), nil)
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// Lock is held for the entire foreground run. Its fd is not inherited by workers.
type Lock struct{ f *os.File }

func (r Repo) Lock() (*Lock, error) {
	if err := os.MkdirAll(r.State, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(r.State, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("repository controller is busy: %w", err)
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Close() error {
	return errors.Join(unix.Flock(int(l.f.Fd()), unix.LOCK_UN), l.f.Close())
}
