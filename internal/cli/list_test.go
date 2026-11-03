package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestWriteStacks(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	stacks := []lifecycle.Stack{
		{WorkspaceID: "1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31", Worktree: "/Users/gabriel/Source/trocker", Status: "running(2)"},
		{WorkspaceID: "9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18", Worktree: "/Users/gabriel/Source/trocker-api-cache", Status: "exited(1), running(1)"},
	}
	if err := writeStacks(&output, stacks, false); err != nil {
		t.Fatal(err)
	}
	want := "WORKSPACE                             STATUS                 WORKTREE\n" +
		"1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31  running(2)             /Users/gabriel/Source/trocker\n" +
		"9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18  exited(1), running(1)  /Users/gabriel/Source/trocker-api-cache\n"
	if output.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", &output, want)
	}
}

func TestWriteStacksWhenEmpty(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		all  bool
		want string
	}{
		{name: "running", want: "No running stacks on this Docker target.\n"},
		{name: "all", all: true, want: "No stacks registered with this Docker target.\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			if err := writeStacks(&output, nil, test.all); err != nil {
				t.Fatal(err)
			}
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestListWithoutRegistrationsDoesNotNeedDocker(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	t.Setenv("XDG_STATE_HOME", directory)
	t.Setenv("LOCALAPPDATA", directory)
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")

	var output bytes.Buffer
	if err := Run([]string{"ls"}, "test", &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "No running stacks on this Docker target.\n" {
		t.Fatalf("output = %q", output.String())
	}
	output.Reset()
	if err := Run([]string{"ls", "--all"}, "test", &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "No stacks registered with this Docker target.\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestWriteStacksReportsMissingWorktrees(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	stacks := []lifecycle.Stack{
		{WorkspaceID: "1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31", Worktree: "/repo/.worktrees/task-1", Status: "running(2)"},
		{WorkspaceID: "9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18", Worktree: "/repo/.worktrees/task-3", Status: "missing, running(2), 1 volume retained", Missing: true, Volumes: 1},
		{WorkspaceID: "5d1c8b7a-3e2f-4a9b-8c6d-0e1f2a3b4c5d", Worktree: "/repo/.worktrees/task-4", Status: "missing, git metadata survives", Missing: true, GitMetadata: true},
	}
	if err := writeStacks(&output, stacks, true); err != nil {
		t.Fatal(err)
	}
	want := "WORKSPACE                             STATUS                                  WORKTREE\n" +
		"1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31  running(2)                              /repo/.worktrees/task-1\n" +
		"9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18  missing, running(2), 1 volume retained  /repo/.worktrees/task-3\n" +
		"5d1c8b7a-3e2f-4a9b-8c6d-0e1f2a3b4c5d  missing, git metadata survives          /repo/.worktrees/task-4\n" +
		"\n" +
		"Run trocker release <workspace> to stop and forget a workspace whose worktree is gone.\n" +
		"Add --volumes to remove the volumes it still owns.\n"
	if output.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", &output, want)
	}
}

func TestWriteStacksOmitsReleaseHintForLiveStacks(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	stacks := []lifecycle.Stack{{WorkspaceID: "1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31", Worktree: "/repo/.worktrees/task-1", Status: "running(1)"}}
	if err := writeStacks(&output, stacks, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "release") {
		t.Fatalf("hint shown for a live workspace:\n%s", &output)
	}
}
