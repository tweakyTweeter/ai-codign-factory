package factory

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func waitExited(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return err
	}
}

func emptyProcessGroup(int) bool { return false }
