package compose

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/compose/v5/cmd/formatter"
	"github.com/docker/compose/v5/pkg/api"
)

func TestComposeProgressRedactsFailures(t *testing.T) {
	var out bytes.Buffer
	progress := composeProgress(&out, false, true)
	progress.Start(t.Context(), "up")
	events := []api.Resource{
		{ID: "Container demo-api-1", Status: api.Working, Text: api.StatusCreating},
		{ID: "Container demo-api-1", Status: api.Error, Text: "secret engine error", Details: "secret configuration value"},
	}
	progress.On(events...)
	progress.Done("up", false)
	if !strings.Contains(out.String(), "Container demo-api-1 Creating") || !strings.Contains(out.String(), "Error") {
		t.Fatalf("native events missing: %s", &out)
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("unsafe plain progress: %q", &out)
	}
	if events[1].Text != "secret engine error" {
		t.Fatal("renderer changed SDK events")
	}
}

func TestProgressWriteFailureCancelsOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := &progressOutput{out: shortProgressWriter{}, cancel: cancel}
	progress := composeProgress(output, false, true)
	progress.Start(ctx, "up")
	progress.On(api.Resource{ID: "Container demo-api-1", Text: api.StatusCreating})
	progress.Done("up", false)
	if !errors.Is(output.Err(), io.ErrShortWrite) || ctx.Err() != context.Canceled {
		t.Fatalf("write failure not propagated: %v, %v", output.Err(), ctx.Err())
	}
}

type shortProgressWriter struct{}

func (shortProgressWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestPlainForegroundStatusHasNoTerminalEscapes(t *testing.T) {
	var out bytes.Buffer
	logs := plainLogConsumer{LogConsumer: formatter.NewLogConsumer(t.Context(), &out, &out, false, true, false)}
	logs.Status("api", "exited with code 0")
	if !strings.Contains(out.String(), "exited with code 0") || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("plain log status: %q", &out)
	}
}
