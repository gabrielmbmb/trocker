//go:build integration

package lifecycle_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
)

const accessCompose = `services:
  app:
    image: busybox:1.37
    command: [sh, -c, 'echo startup; echo startup-error >&2; exec sleep 300']
`

func TestContainerInspectionAndExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, _ := workspaceFixture(t, ctx, accessCompose, "version: 1\ncompose: [compose.yaml]\n")
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	before := snapshotObservationFiles(t, main, store.Directory)

	inspection, err := lifecycle.Containers(ctx, main, store, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Containers) != 1 || inspection.Containers[0].Service != "app" || inspection.Containers[0].State != "running" || inspection.Containers[0].Container != registration.ProjectName()+"-app-1" || inspection.Project != registration.ProjectName() {
		t.Fatalf("unexpected containers: %+v", inspection)
	}
	if selected, err := lifecycle.Containers(ctx, main, store, []string{"missing"}, true); err != nil || len(selected.Containers) != 0 || selected.Containers == nil {
		t.Fatalf("missing service selection: %+v, %v", selected, err)
	}

	logs := waitForLogs(t, ctx, main, store, "startup")
	if !strings.Contains(logs, "startup-error") {
		t.Fatalf("stderr log missing: %q", logs)
	}
	if err := lifecycle.Logs(ctx, main, store, compose.LogOptions{Services: []string{"missing"}}, io.Discard); err == nil || !strings.Contains(err.Error(), `service "missing" has no container`) {
		t.Fatalf("missing service logs: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code, err := lifecycle.Exec(ctx, main, store, compose.ExecOptions{
		Service:     "app",
		Command:     []string{"sh", "-c", `printf '%s' "$DEMO"; printf err >&2; exit 7`},
		Environment: []string{"DEMO=value"},
	}, compose.ExecStreams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr})
	if err != nil || code != 7 || stdout.String() != "value" || stderr.String() != "err" {
		t.Fatalf("exec: code=%d stdout=%q stderr=%q err=%v", code, &stdout, &stderr, err)
	}
	if _, err := lifecycle.Exec(ctx, main, store, compose.ExecOptions{Service: "missing", Command: []string{"true"}}, compose.ExecStreams{Out: io.Discard, Err: io.Discard}); err == nil || !strings.Contains(err.Error(), `service "missing" has no container`) {
		t.Fatalf("missing service exec: %v", err)
	}

	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Stop, Services: []string{"app"}}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if running, err := lifecycle.Containers(ctx, main, store, nil, false); err != nil || len(running.Containers) != 0 {
		t.Fatalf("stopped service remained visible by default: %+v, %v", running, err)
	}
	if all, err := lifecycle.Containers(ctx, main, store, nil, true); err != nil || len(all.Containers) != 1 || all.Containers[0].State != "exited" {
		t.Fatalf("stopped service missing from --all: %+v, %v", all, err)
	}
	if logs := waitForLogs(t, ctx, main, store, "startup"); !strings.Contains(logs, "startup") {
		t.Fatalf("stopped container logs missing: %q", logs)
	}
	if _, err := lifecycle.Exec(ctx, main, store, compose.ExecOptions{Service: "app", Command: []string{"true"}}, compose.ExecStreams{Out: io.Discard, Err: io.Discard}); err == nil || !strings.Contains(err.Error(), `service "app" is not running`) {
		t.Fatalf("stopped service exec: %v", err)
	}

	after := snapshotObservationFiles(t, main, store.Directory)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("inspection commands changed workspace or registration files")
	}

	if err := os.Remove(filepath.Join(main, "trocker.yaml")); err != nil {
		t.Fatal(err)
	}
	if all, err := lifecycle.Containers(ctx, main, store, nil, true); err != nil || len(all.Containers) != 1 || all.Containers[0].State != "exited" {
		t.Fatalf("containers without a manifest: %+v, %v", all, err)
	}
}

func waitForLogs(t *testing.T, ctx context.Context, directory string, store state.Store, want string) string {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var output bytes.Buffer
		if err := lifecycle.Logs(ctx, directory, store, compose.LogOptions{Tail: "all"}, &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), want) {
			return output.String()
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("logs did not contain %q: %q", want, &output)
		case <-ticker.C:
		}
	}
}
