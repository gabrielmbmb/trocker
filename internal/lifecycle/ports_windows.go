package lifecycle

import "golang.org/x/sys/windows"

// Windows network operations return Winsock codes, not syscall.EADDRINUSE.
const addressInUse = windows.WSAEADDRINUSE
