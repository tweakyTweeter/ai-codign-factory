package factory

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// NOTE_EXIT observes the owned child's exit without wait/reaping. Registering
// an already-exited child may report ESRCH; it is still unreaped, so its PID is
// reserved and group teardown remains safe. Darwin disallows raw waitid traps
// on some releases, hence the native kqueue interface rather than unsafe traps.
func waitExited(pid int) error {
	fd, err := unix.Kqueue()
	if err != nil {
		return err
	}

	defer unix.Close(fd)
	unix.CloseOnExec(fd)
	change := []unix.Kevent_t{{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT,
	}}
	var events [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(fd, change, events[:], nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		change = nil
		if n > 0 {
			if events[0].Flags&unix.EV_ERROR != 0 && events[0].Data != 0 && events[0].Data != int64(syscall.ESRCH) {
				return syscall.Errno(events[0].Data)
			}
			return nil
		}
	}
}

// Darwin may return EPERM, rather than ESRCH, for a group containing only an
// exited zombie. Verify there are no live members before ignoring that error.
func emptyProcessGroup(pgid int) bool {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return false
	}
	for _, p := range procs {
		if p.Proc.P_stat != 5 /* SZOMB */ {
			return false
		}
	}
	return true
}
