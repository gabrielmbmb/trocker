package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/plan"
)

func TestParsePublication(t *testing.T) {
	for _, test := range []struct {
		value string
		want  plan.PortKey
		ok    bool
	}{
		{value: "kennedy-api:8000", want: plan.PortKey{Service: "kennedy-api", Port: 8000}, ok: true},
		{value: "kennedy-api"},
		{value: ":8000"},
		{value: "kennedy-api:0"},
		{value: "kennedy-api:65536"},
	} {
		if got, ok := parsePublication(test.value); got != test.want || ok != test.ok {
			t.Errorf("parsePublication(%q) = %v, %v; want %v, %v", test.value, got, ok, test.want, test.ok)
		}
	}
}

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
