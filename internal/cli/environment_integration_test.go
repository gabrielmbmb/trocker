//go:build integration && !windows

package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/compose"
)

const (
	wrappedCompose = `services:
  api:
    image: busybox:1.37
    command: [sleep, '300']
    environment:
      FROM_WRAPPER: ${FROM_WRAPPER:?only the environment wrapper sets this}
`
	wrappedManifest = "version: 1\ncompose: [compose.yaml]\nenvironment_wrapper: [env.sh]\n"
	wrapperScript   = "#!/bin/sh\necho ran >> \"$WRAPPER_LOG\"\nexport FROM_WRAPPER=yes\nexec \"$@\"\n"
)

// Compose here fails to load without the wrapper's variable, so each command
// that succeeds proves the environment reached it. Commands run without a
// terminal, as an agent runs them, and the count shows the wrapper ran exactly
// once per command rather than looping or being skipped.
func TestEnvironmentWrapperProvidesComposeEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	binary := buildTrocker(t, ctx)
	checkout := wrappedCheckout(t, ctx)
	runs := filepath.Join(t.TempDir(), "wrapper.log")
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := target.Host
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "DOCKER_HOST=" + host, "WRAPPER_LOG=" + runs}
	trocker := func(args ...string) (string, string, error) {
		command := exec.CommandContext(ctx, binary, args...) //nolint:gosec // Runs the binary this test built.
		command.Dir, command.Env = checkout, environment
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		return stdout.String(), stderr.String(), err
	}
	wrapperRuns := func() int {
		content, err := os.ReadFile(runs) //nolint:gosec // Test-owned log.
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(content), "ran\n")
	}

	if stdout, stderr, err := trocker("init", "--dry-run"); err != nil || wrapperRuns() != 1 || !strings.Contains(stdout, "Wrapper    env.sh") {
		t.Fatalf("dry run: %v, runs %d\n%s%s", err, wrapperRuns(), stdout, stderr)
	}
	if _, stderr, err := trocker("plan"); err == nil || !strings.Contains(stderr, "not registered") {
		t.Fatalf("a dry run must not register the workspace: %v\n%s", err, stderr)
	}
	if _, stderr, err := trocker("init"); err != nil || wrapperRuns() != 3 {
		t.Fatalf("init: %v, runs %d\n%s", err, wrapperRuns(), stderr)
	}
	if _, stderr, err := trocker("plan"); err != nil || wrapperRuns() != 4 {
		t.Fatalf("plan: %v, runs %d\n%s", err, wrapperRuns(), stderr)
	}
}

func buildTrocker(t *testing.T, ctx context.Context) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "trocker")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "github.com/gabrielmbmb/trocker/cmd/trocker") //nolint:gosec // Builds this module into a test-owned path.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build trocker: %v\n%s", err, output)
	}
	return binary
}

func wrappedCheckout(t *testing.T, ctx context.Context) string {
	t.Helper()
	checkout := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture"},
	} {
		command := exec.CommandContext(ctx, "git", args...) //nolint:gosec // Fixed git operations on a temporary repository.
		command.Dir = checkout
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	for name, content := range map[string]string{"compose.yaml": wrappedCompose, "trocker.yaml": wrappedManifest, "env.sh": wrapperScript} {
		if err := os.WriteFile(filepath.Join(checkout, name), []byte(content), 0o700); err != nil { //nolint:gosec // The wrapper must be executable.
			t.Fatal(err)
		}
	}
	return checkout
}
