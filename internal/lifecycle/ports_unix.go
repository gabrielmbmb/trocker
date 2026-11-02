//go:build !windows

package lifecycle

import "syscall"

const addressInUse = syscall.EADDRINUSE
