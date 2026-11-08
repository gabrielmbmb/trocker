package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestWritePorts(t *testing.T) {
	var output bytes.Buffer
	result := lifecycle.PortResult{Ports: []lifecycle.PortEntry{
		{Service: "api", ContainerPort: 8000, HostPort: 24001},
		{Service: "web", ContainerPort: 3000, HostPort: 24002},
	}}
	if err := writePorts(&output, result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SERVICE", "CONTAINER PORT", "HOST PORT", "api", "8000", "24001"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
	output.Reset()
	if err := writePorts(&output, lifecycle.PortResult{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No stable host ports") {
		t.Fatalf("empty output = %q", output.String())
	}
}
