package native

import (
	"errors"

	"golang.org/x/sys/unix"
)

func normalizeGroupError(_ int, err error) error { return err }

func awaitExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
