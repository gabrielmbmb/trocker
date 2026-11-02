package native

import (
	"fmt"
	"runtime"
)

// CheckSupport runs before workflows that may publish native configuration.
func CheckSupport() error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return fmt.Errorf("native execution currently requires Linux or macOS")
	}
	return nil
}
