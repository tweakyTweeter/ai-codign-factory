package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const outputLimit = 1 << 20

var ErrTeardown = errors.New("process cleanup could not be confirmed")

// cappedWriter drains all output but stores at most limit bytes. It is safe for
// concurrent stdout/stderr writes. Exceeding the cap does not block the child.
type cappedWriter struct {
	mu        sync.Mutex
	dst       io.Writer
	remaining int
	truncated bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	keep := min(n, w.remaining)
	if keep < n {
		w.truncated = true
	}
	if keep > 0 {
		if _, err := w.dst.Write(p[:keep]); err != nil {
			return 0, err
		}
		w.remaining -= keep
	}
	return n, nil
}

// execute owns the child until reaping it. waitExited observes exit WITHOUT
// reaping: the leader's PID remains reserved, so -PID can never name a reused
// process group. Group teardown precedes Wait, including after normal exit.
// A deliberately daemonizing/setsid child is outside this trust boundary.
func execute(ctx context.Context, dir string, argv, env []string, out io.Writer, started func(int) error) (int, error) {
	return executeGroup(ctx, dir, argv, env, out, started, true)
}

// Adapter children inherit the controller-owned process group. Starting another
// group here would let a nested Copilot survive a killed adapter.
func executeGroup(ctx context.Context, dir string, argv, env []string, out io.Writer, started func(int) error, ownGroup bool) (int, error) {
	return executeStreams(ctx, dir, argv, env, out, out, started, ownGroup)
}

func executeStreams(ctx context.Context, dir string, argv, env []string, stdout, stderr io.Writer, started func(int) error, ownGroup bool) (int, error) {
	return executeStreamsEnv(ctx, dir, argv, env, stdout, stderr, started, ownGroup, true)
}

// executeStreamsEnv runs argv with env merged over the adapter environment when
// inherit is true, or with exactly env (nothing inherited) when it is false.
func executeStreamsEnv(ctx context.Context, dir string, argv, env []string, stdout, stderr io.Writer, started func(int) error, ownGroup, inherit bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if inherit {
		cmd.Env = mergedEnv(env)
	} else {
		cmd.Env = append([]string{}, env...)
	}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: ownGroup}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start executable: %w", err)
	}
	killGroup := func() error {
		target := cmd.Process.Pid
		if ownGroup {
			target = -target
		}
		err := syscall.Kill(target, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if ownGroup && errors.Is(err, syscall.EPERM) && emptyProcessGroup(cmd.Process.Pid) {
			return nil
		}
		if err != nil {
			pgid, _ := syscall.Getpgid(cmd.Process.Pid)
			return fmt.Errorf("pid %d pgid %d: %w", cmd.Process.Pid, pgid, err)
		}
		return err
	}
	var cause error
	if started != nil {
		cause = started(cmd.Process.Pid)
	}
	exited := make(chan error, 1)
	go func() {
		err := waitExited(cmd.Process.Pid)
		if err != nil {
			err = fmt.Errorf("observe owned child exit: %w", err)
		}
		exited <- err
	}()
	if cause == nil {
		select {
		case cause = <-exited:
			exited = nil
		case <-ctx.Done():
			cause = ctx.Err()
		}
	}
	killErr := killGroup()
	if killErr != nil {
		killErr = errors.Join(ErrTeardown, fmt.Errorf("tear down owned process group: %w", killErr))
	}
	if exited != nil {
		// SIGKILL makes the non-reaping observer return before Wait is allowed
		// to release the PID identity.
		cause = errors.Join(cause, <-exited)
	}
	waitErr := cmd.Wait()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		waitErr = errors.Join(ErrTeardown, waitErr)
	}
	return code, errors.Join(cause, killErr, waitErr)
}

func mergedEnv(extra []string) []string {
	keys := map[string]bool{}
	for _, entry := range extra {
		key, _, _ := strings.Cut(entry, "=")
		keys[key] = true
	}
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !keys[key] && !strings.HasPrefix(key, "FACTORY_") {
			env = append(env, entry)
		}
	}
	return append(env, extra...)
}

func capture(ctx context.Context, dir string, argv, env []string) ([]byte, error) {
	return captureGroup(ctx, dir, argv, env, true)
}

func captureGroup(ctx context.Context, dir string, argv, env []string, ownGroup bool) ([]byte, error) {
	var b bytes.Buffer
	w := &cappedWriter{dst: &b, remaining: outputLimit}
	_, err := executeGroup(ctx, dir, argv, env, w, nil, ownGroup)
	if w.truncated {
		err = errors.Join(err, errors.New("command output exceeded 1 MiB"))
	}
	return b.Bytes(), err
}
