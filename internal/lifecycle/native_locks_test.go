//go:build linux || darwin

package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/native"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func assertNativeDoesNotHoldLocks(t *testing.T, main string, store state.Store, gitDir string) {
	t.Helper()
	t.Setenv("TROCKER_TEST_NATIVE_WAIT", "1")
	defer t.Setenv("TROCKER_TEST_NATIVE_WAIT", "")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	input, send := io.Pipe()
	output, receive := io.Pipe()
	defer func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	}()
	defer func() {
		if err := send.Close(); err != nil {
			t.Error(err)
		}
	}()
	args := nativeArguments(t)
	done := make(chan error, 1)
	go func() {
		err := lifecycle.Run(ctx, main, store, "app", args, native.Streams{In: input, Out: receive, Err: io.Discard})
		done <- errors.Join(err, receive.Close())
	}()
	var ready nativeReport
	if err := json.NewDecoder(output).Decode(&ready); err != nil {
		t.Fatal("child readiness", err)
	}
	locking, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	lock, err := store.LockWorkspace(locking, gitDir)
	if err != nil {
		t.Fatal("native lifetime blocked workspace mutations", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(locking, func(*state.Registry) error { return nil }); err != nil {
		t.Fatal("native lifetime blocked registry", err)
	}
	if ready.ConfigPath != "" {
		data := readNativeConfig(t, ready.ConfigPath)
		paths, err := store.PublishGenerated(locking, ready.ID, "", "app", map[string][]byte{filepath.Base(ready.ConfigPath): data})
		if err != nil || paths[filepath.Base(ready.ConfigPath)] != ready.ConfigPath {
			t.Fatal("native lifetime blocked generation publication", err)
		}
		changed, err := store.PublishGenerated(locking, ready.ID, "", "app", map[string][]byte{filepath.Base(ready.ConfigPath): append(data, '\n')})
		if err != nil || changed[filepath.Base(ready.ConfigPath)] == ready.ConfigPath {
			t.Fatal("changed publication failed during native lifetime", err)
		}
	}
	if err := send.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
