package native

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Darwin killpg can return EPERM for a group containing only zombies.
// Verify the owned, unreaped group rather than ignoring permission errors.
func normalizeGroupError(pid int, err error) error {
	if !errors.Is(err, unix.EPERM) {
		return err
	}
	members, inspectErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if inspectErr != nil {
		return err
	}
	const zombie = 5 // SZOMB in Darwin's sys/proc.h.
	for _, member := range members {
		if member.Proc.P_stat != zombie {
			return err
		}
	}
	return nil
}

func awaitExit(pid int) (err error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return err
	}
	unix.CloseOnExec(queue)
	defer func() { err = errors.Join(err, unix.Close(queue)) }()
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT} //nolint:gosec // A started child has a positive PID.
	_, err = unix.Kevent(queue, []unix.Kevent_t{change}, nil, nil)
	// An already-exited leader remains our unreaped child, not a reusable PID.
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	events := make([]unix.Kevent_t, 1)
	for {
		_, err = unix.Kevent(queue, nil, events, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
