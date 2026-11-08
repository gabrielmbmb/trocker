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

// The wrapper is repository code that Trocker runs before Compose loads, so it
// must never run before init approves it, and an edit withdraws the approval.
// Compose here fails to load without the wrapper's variable, so a successful
// plan proves the environment reached it.
func TestEnvironmentWrapperRunsOnlyOnceApproved(t *testing.T) {
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

	if _, stderr, err := trocker("plan"); err == nil || !strings.Contains(stderr, "is not approved") || wrapperRuns() != 0 {
		t.Fatalf("unapproved plan: %v, runs %d\n%s", err, wrapperRuns(), stderr)
	}
	digest, stderr, err := trocker("init", "--dry-run")
	if err != nil || wrapperRuns() != 0 || !strings.Contains(stderr, "Wrapper    env.sh") {
		t.Fatalf("dry run: %v, runs %d\n%s", err, wrapperRuns(), stderr)
	}
	if _, stderr, err := trocker("init", "--confirm", strings.TrimSpace(digest)); err != nil || wrapperRuns() != 1 {
		t.Fatalf("confirmed init: %v, runs %d\n%s", err, wrapperRuns(), stderr)
	}
	if _, stderr, err := trocker("plan"); err != nil || wrapperRuns() != 2 {
		t.Fatalf("approved plan: %v, runs %d\n%s", err, wrapperRuns(), stderr)
	}

	script := filepath.Join(checkout, "env.sh")
	if err := os.WriteFile(script, []byte(wrapperScript+"# edited\n"), 0o700); err != nil { //nolint:gosec // The wrapper must be executable.
		t.Fatal(err)
	}
	if _, stderr, err := trocker("plan"); err == nil || !strings.Contains(stderr, "is not approved") || wrapperRuns() != 2 {
		t.Fatalf("plan after an edit: %v, runs %d\n%s", err, wrapperRuns(), stderr)
	}
	if edited, _, err := trocker("init", "--dry-run"); err != nil || edited == digest {
		t.Fatalf("an edited wrapper must need a new digest: %v", err)
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
