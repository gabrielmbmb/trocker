package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/config"
)

const (
	base     = "version: 1\ncompose: [compose.yaml]\n"
	endpoint = "endpoints:\n  api:\n    service: backend\n    port: 8000\n    scheme: http\n"
)

func TestManifestValidation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, input, field string }{
		{"empty", "", "manifest"},
		{"root sequence", "[]", "manifest"},
		{"missing version", "compose: [compose.yaml]", "version"},
		{"unsupported version", "version: 2\ncompose: [compose.yaml]", "version"},
		{"unknown field", base + "secret_field: do-not-print", "manifest"},
		{"duplicate field", base + "version: 1", "manifest"},
		{"multiple documents", base + "---\n" + base, "manifest"},
		{"malformed YAML", base + "endpoints: [", "manifest"},
		{"missing compose", "version: 1", "compose"},
		{"null compose", "version: 1\ncompose: null", "compose"},
		{"scalar compose", "version: 1\ncompose: compose.yaml", "manifest"},
		{"empty compose", "version: 1\ncompose: []", "compose"},
		{"empty path", "version: 1\ncompose: [' ']", "compose[0]"},
		{"null path", "version: 1\ncompose: [null]", "compose"},
		{"invalid alias", base + strings.ReplaceAll(endpoint, "api:", "bad.alias:"), "endpoints"},
		{"missing service", base + strings.ReplaceAll(endpoint, "    service: backend\n", ""), "endpoints.api.service"},
		{"null endpoint", base + "endpoints: {api: null}", "endpoints.api.service"},
		{"zero port", base + strings.ReplaceAll(endpoint, "8000", "0"), "endpoints.api.port"},
		{"negative port", base + strings.ReplaceAll(endpoint, "8000", "-1"), "manifest"},
		{"overflow port", base + strings.ReplaceAll(endpoint, "8000", "65536"), "manifest"},
		{"unsupported scheme", base + strings.ReplaceAll(endpoint, "http", "ftp"), "endpoints.api.scheme"},
		{"unknown nested field", base + endpoint + "    secret: do-not-print", "manifest"},
		{"empty binding service", base + "bindings: {'': {}}", "bindings"},
		{"invalid environment name", base + "bindings: {web: {environment: {'BAD-NAME': value}}}", "bindings.web.environment"},
		{"nested environment value", base + "bindings: {web: {environment: {TOKEN: [do-not-print]}}}", "manifest"},
		{"unknown native field", base + "bindings: {web: {native: {secret: do-not-print}}}", "manifest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest, err := config.Parse([]byte(test.input))
			var validation *config.Error
			if !errors.As(err, &validation) || !strings.HasPrefix(validation.Field, test.field) {
				t.Fatalf("error = %v, want config error at %s", err, test.field)
			}
			if manifest != nil {
				t.Fatal("invalid manifest returned a partial result")
			}
			if strings.Contains(err.Error(), "do-not-print") {
				t.Fatal("error exposed a value")
			}
		})
	}
}

func TestParsePreservesExplicitConfiguration(t *testing.T) {
	t.Parallel()
	manifest, err := config.Parse([]byte(`
version: 1
compose: [base.yaml, override.yaml]
endpoints:
  api:
    service: backend
    port: 8000
    scheme: https
bindings:
  web:
    environment:
      URL: "{{ endpoints.api.container.url }}"
      EMPTY: ""
    native:
      environment:
        URL: "{{ endpoints.api.host.url }}"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := &config.Manifest{
		Version:   1,
		Compose:   []string{"base.yaml", "override.yaml"},
		Endpoints: map[string]config.Endpoint{"api": {Service: "backend", Port: 8000, Scheme: "https"}},
		Bindings: map[string]config.Bindings{"web": {
			Environment: map[string]string{"URL": "{{ endpoints.api.container.url }}", "EMPTY": ""},
			Native:      config.NativeBindings{Environment: map[string]string{"URL": "{{ endpoints.api.host.url }}"}},
		}},
	}
	if !reflect.DeepEqual(manifest, want) {
		t.Fatal("parsing changed file order, endpoint settings, or explicit container/native bindings")
	}
}

func TestBindings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, value   string
		native, valid bool
	}{
		{name: "literal", value: "hello {world}", valid: true},
		{name: "empty", valid: true},
		{name: "multiple", value: "id-{{ workspace.id }}:{{ endpoints.api.container.port }}", valid: true},
		{name: "host", value: "{{endpoints.api.host.url}}", native: true, valid: true},
		{name: "container host binding", value: "{{ endpoints.api.host.url }}", valid: true},
		{name: "removed public URL", value: "{{ endpoints.api.public.url }}"},
		{name: "removed public host", value: "{{ endpoints.api.public.host }}"},
		{name: "removed public port", value: "{{ endpoints.api.public.port }}"},
		{name: "removed native public", value: "{{ endpoints.api.public.url }}", native: true},
		{name: "unknown endpoint", value: "{{ endpoints.other.host.url }}"},
		{name: "unknown component", value: "{{ endpoints.api.host.ws_url }}"},
		{name: "unknown representation", value: "{{ endpoints.api.remote.url }}"},
		{name: "native container", value: "{{ endpoints.api.container.url }}", native: true},
		{name: "missing close", value: "{{ workspace.id"},
		{name: "missing open", value: "workspace.id }}"},
		{name: "empty reference", value: "{{ }}"},
		{name: "nested reference", value: "{{ {{ workspace.id }} }}"},
		{name: "function", value: "{{ env SECRET }}"},
		{name: "invalid second reference", value: "{{ workspace.id }}-{{ endpoints.missing.host.url }}"},
		{name: "trailing unmatched delimiter", value: "{{ workspace.id }}-{{"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := config.Manifest{
				Version:   1,
				Compose:   []string{"compose.yaml"},
				Endpoints: map[string]config.Endpoint{"api": {Service: "backend", Port: 8000, Scheme: "http"}},
			}
			binding := config.Bindings{Environment: map[string]string{"URL": test.value}}
			if test.native {
				binding.Native.Environment = binding.Environment
				binding.Environment = nil
			}
			manifest.Bindings = map[string]config.Bindings{"web": binding}
			err := manifest.Validate()
			if (err == nil) != test.valid {
				t.Fatalf("valid = %v, error = %v", test.valid, err)
			}
			if err != nil {
				field := "bindings.web.environment.URL"
				if test.native {
					field = "bindings.web.native.environment.URL"
				}
				var validation *config.Error
				if !errors.As(err, &validation) || validation.Field != field {
					t.Fatalf("error = %v, want error at %s", err, field)
				}
			}
		})
	}
}

func TestReservedIdentityAndTCPReferences(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, scheme, key, value string
		valid                    bool
	}{
		{"identity", "http", "TROCKER_WORKSPACE_ID", "{{ workspace.id }}", true},
		{"identity literal", "http", "TROCKER_WORKSPACE_ID", "other", false},
		{"identity prefix", "http", "TROCKER_WORKSPACE_ID", "prefix-{{ workspace.id }}", false},
		{"tcp host", "tcp", "HOST", "{{ endpoints.api.host.host }}", true},
		{"tcp URL", "tcp", "URL", "{{ endpoints.api.host.url }}", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			m := config.Manifest{Version: 1, Compose: []string{"compose.yaml"}, Endpoints: map[string]config.Endpoint{"api": {Service: "backend", Port: 8000, Scheme: test.scheme}}, Bindings: map[string]config.Bindings{"web": {Environment: map[string]string{test.key: test.value}}}}
			if err := m.Validate(); (err == nil) != test.valid {
				t.Fatalf("valid = %v, error = %v", test.valid, err)
			}
		})
	}
}

func TestLoadResolvesPathsWithoutChangingFiles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "trocker.yaml")
	content := []byte("version: 1\ncompose: ['nested folder/compose.yaml']\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Directory != directory || manifest.Compose[0] != filepath.Join(directory, "nested folder", "compose.yaml") {
		t.Fatalf("unexpected paths: %#v", manifest)
	}
	after, err := os.ReadFile(path) //nolint:gosec // Path is a test-owned file in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(content) {
		t.Fatal("loader modified manifest")
	}
	_, err = config.Load(filepath.Join(directory, "missing.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want underlying not-exist error", err)
	}
}

func TestBindingErrorsDoNotExposeValues(t *testing.T) {
	t.Parallel()
	for _, native := range []bool{false, true} {
		environment := "environment"
		if native {
			environment = "native.environment"
		}
		t.Run(environment, func(t *testing.T) {
			t.Parallel()
			for _, test := range []struct{ key, value string }{
				{"TOKEN", "do-not-print-{{ endpoints.unknown.host.url }}"},
				{"TROCKER_WORKSPACE_ID", "do-not-print"},
			} {
				binding := config.Bindings{Environment: map[string]string{test.key: test.value}}
				if native {
					binding.Native.Environment = binding.Environment
					binding.Environment = nil
				}
				manifest := config.Manifest{
					Version: 1, Compose: []string{"compose.yaml"},
					Bindings: map[string]config.Bindings{"web": binding},
				}
				err := manifest.Validate()
				var validation *config.Error
				field := "bindings.web." + environment + "." + test.key
				if !errors.As(err, &validation) || validation.Field != field {
					t.Fatalf("error = %v, want validation error at %s", err, field)
				}
				if strings.Contains(err.Error(), "do-not-print") {
					t.Fatal("validation exposed a binding value")
				}
			}
		})
	}
}

func TestDecodeErrorHasSafeLocation(t *testing.T) {
	t.Parallel()
	_, err := config.Parse([]byte(base + "bindings: {web: {environment: {TOKEN: [do-not-print]}}}"))
	if err == nil || !strings.Contains(err.Error(), "line 3") || strings.Contains(err.Error(), "do-not-print") {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
}
