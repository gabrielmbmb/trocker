package compose

import (
	"context"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/cmd/formatter"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/term"
)

func interactiveProgress(out io.Writer) bool {
	_, terminal := term.GetFdInfo(out)
	return terminal && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && os.Getenv("CI") == ""
}

func composeProgress(out io.Writer, interactive, detached bool) api.EventProcessor {
	var renderer api.EventProcessor
	if interactive {
		renderer = display.Full(out, out, detached)
	} else {
		renderer = display.Plain(out)
	}
	return safeProgress{EventProcessor: renderer}
}

// SDK failure events can include raw engine/build errors with interpolated
// secrets. Keep native rendering, but let our safe diagnostic explain failures.
type safeProgress struct{ api.EventProcessor }

func (p safeProgress) On(events ...api.Resource) {
	events = slices.Clone(events)
	for i := range events {
		switch events[i].Status {
		case api.Error:
			events[i].Text = "Error"
			events[i].Details = ""
		case api.Warning:
			events[i].Text = "Warning"
			events[i].Details = ""
		}
	}
	p.EventProcessor.On(events...)
}

// Compose's renderers ignore write errors, and progress/log callbacks may run
// concurrently. Serialize writes and cancel the operation on a broken stream.
type progressOutput struct {
	mu     sync.Mutex
	out    io.Writer
	err    error
	cancel context.CancelFunc
}

func (p *progressOutput) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return 0, p.err
	}
	n, err := p.out.Write(data)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		p.err = err
		p.cancel()
	}
	return n, err
}
func (p *progressOutput) Err() error { p.mu.Lock(); defer p.mu.Unlock(); return p.err }

// The pinned Compose log formatter emits a terminal line-reset from Status
// even with color disabled. Route that one callback as a log in plain mode.
type plainLogConsumer struct{ api.LogConsumer }

func (p plainLogConsumer) Status(name, message string) { p.Log(name, message) }

func composeLogs(ctx context.Context, out io.Writer, interactive bool) api.LogConsumer {
	logs := formatter.NewLogConsumer(ctx, out, out, interactive, true, false)
	if !interactive {
		logs = plainLogConsumer{LogConsumer: logs}
	}
	return &lockedLogConsumer{LogConsumer: logs}
}

// Compose v5.6.1 mutates presenter prefixes as new containers are registered.
// Its monitor and attach goroutines invoke callbacks concurrently; serializing
// only the output writer is too late to protect that formatter state.
type lockedLogConsumer struct {
	api.LogConsumer
	mu sync.Mutex
}

func (l *lockedLogConsumer) Log(name, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.LogConsumer.Log(name, message)
}

func (l *lockedLogConsumer) Err(name, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.LogConsumer.Err(name, message)
}

func (l *lockedLogConsumer) Status(name, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.LogConsumer.Status(name, message)
}
