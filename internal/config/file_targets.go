package config

import (
	"path"
	"strings"
)

func ValidFileTarget(target string) bool {
	if target == "/" || !strings.HasPrefix(target, "/") || path.Clean(target) != target || strings.ContainsAny(target, "\\:\x00") {
		return false
	}
	for _, special := range []string{"/proc", "/sys", "/dev", "/etc/hosts", "/etc/hostname", "/etc/resolv.conf"} {
		if PathsOverlap(target, special) {
			return false
		}
	}
	return true
}

// PathsOverlap includes ancestor mounts: a mounted directory can hide a file.
func PathsOverlap(a, b string) bool {
	a = path.Clean(a)
	b = path.Clean(b)
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/")
}
