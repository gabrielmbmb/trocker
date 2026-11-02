package cli_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/cli"
	"github.com/gabrielmbmb/trocker/internal/compose"
)

func TestMissingComposeDiagnostic(t *testing.T) {
	for _, name := range []string{"COMPOSE_FILE", "COMPOSE_ENV_FILES", "COMPOSE_PROJECT_NAME", "COMPOSE_PROFILES"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("COMPOSE_DISABLE_ENV_FILE", "true")
	_, err := compose.DiscoverFiles(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("discovery without Compose configuration succeeded")
	}
	var output bytes.Buffer
	if err := cli.WriteError(&output, fmt.Errorf("%w", err)); err != nil {
		t.Fatal(err)
	}
	want := "error: no Compose file found\n\n" +
		"  help: add compose.yaml to your checkout, then run trocker init\n" +
		"  note: trocker init can generate trocker.yaml after confirmation\n"
	if output.String() != want {
		t.Fatalf("diagnostic:\n%s\nwant:\n%s", &output, want)
	}
}

func TestPlainDiagnostic(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	if err := cli.WriteError(&output, errors.New("operation failed")); err != nil {
		t.Fatal(err)
	}
	if output.String() != "error: operation failed\n" {
		t.Fatalf("unexpected empty details or terminal formatting: %q", &output)
	}
}
