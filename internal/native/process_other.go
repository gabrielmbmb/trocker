//go:build !linux && !darwin

package native

import (
	"context"
	"os"
)

func Run(context.Context, Options) error {
	return CheckSupport()
}
func forwardedSignals() []os.Signal { return []os.Signal{os.Interrupt} }
