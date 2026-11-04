package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestWriteContainers(t *testing.T) {
	t.Parallel()

	result := lifecycle.ContainerResult{
		WorkspaceID: "1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31",
		Project:     "trocker-1b2f9a4c7d3e4b219c8a6f0a2d5e8b31",
		Containers: []lifecycle.ServiceStatus{
			{Service: "api", Container: "project-api-1", State: "running", Health: "healthy"},
			{Service: "worker", Container: "project-worker-1", State: "exited"},
		},
	}
	var output bytes.Buffer
	if err := writeContainers(&output, result, true, nil); err != nil {
		t.Fatal(err)
	}
	want := "SERVICE  CONTAINER         STATE    HEALTH\n" +
		"api      project-api-1     running  healthy\n" +
		"worker   project-worker-1  exited   -\n"
	if output.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", &output, want)
	}
}

func TestWriteContainersWhenEmpty(t *testing.T) {
	t.Parallel()

	result := lifecycle.ContainerResult{WorkspaceID: "1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31"}
	for _, test := range []struct {
		name     string
		all      bool
		services []string
		want     string
	}{
		{name: "running", want: "No running containers for workspace 1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31. Add --all to include stopped containers.\n"},
		{name: "all", all: true, want: "No containers for workspace 1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31.\n"},
		{name: "service selection", services: []string{"api", "web"}, want: "No running containers for services api, web. Add --all to include stopped containers.\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			if err := writeContainers(&output, result, test.all, test.services); err != nil {
				t.Fatal(err)
			}
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestExecArgumentValidation(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"exec"},
		{"exec", "api"},
		{"exec", "", "echo"},
		{"exec", "api", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			err := Run(args, "dev", &output)
			if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "SERVICE COMMAND") {
				t.Fatalf("error = %v, want exec usage error", err)
			}
			if output.Len() != 0 {
				t.Errorf("invalid invocation wrote success output: %q", output.String())
			}
		})
	}
}

func TestExecTreatsFlagsAfterServiceAsCommandArguments(t *testing.T) {
	t.Chdir(t.TempDir())

	var output bytes.Buffer
	err := Run([]string{"exec", "api", "printf", "--help"}, "dev", &output)
	if err == nil {
		t.Fatal("unregistered invocation unexpectedly succeeded")
	}
	if errors.Is(err, ErrUsage) {
		t.Fatalf("command flags were parsed as Trocker flags: %v", err)
	}
}
