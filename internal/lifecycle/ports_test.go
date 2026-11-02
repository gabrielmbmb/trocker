package lifecycle

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/state"
)

func TestClaimAvailablePortRespectsListenersAndLeases(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		t.Run(host, func(t *testing.T) {
			store := state.Store{Directory: filepath.Join(t.TempDir(), "state")}
			var listen net.ListenConfig
			listener, err := listen.Listen(t.Context(), "tcp4", net.JoinHostPort(host, "0"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := listener.Close(); err != nil {
					t.Error(err)
				}
			}()
			address, ok := listener.Addr().(*net.TCPAddr)
			if !ok {
				t.Fatal("unexpected listener address")
			}
			preferred := uint16(address.Port) //nolint:gosec // A bound TCP port is in 1..65535.
			first, lease, err := claimAvailablePort(t.Context(), store, preferred, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := lease.Close(); err != nil {
					t.Error(err)
				}
			}()
			if first == preferred {
				t.Fatal("existing listener ignored")
			}
			second, other, err := claimAvailablePort(t.Context(), store, first, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := other.Close(); err != nil {
				t.Fatal(err)
			}
			if second == first || second == preferred {
				t.Fatal("pending claim or existing listener ignored")
			}
		})
	}
}

func TestCanceledAllocationDoesNotCreateClaims(t *testing.T) {
	store := state.Store{Directory: filepath.Join(t.TempDir(), "state")}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := claimAvailablePort(ctx, store, 0, false, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation", err)
	}
	if _, err := os.Stat(store.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled allocation created files", err)
	}
}
