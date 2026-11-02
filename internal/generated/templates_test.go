package generated_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/generated"
)

func TestTemplateRenderingAndFailures(t *testing.T) {
	directory := t.TempDir()
	definition := config.GeneratedFile{Template: "config.tmpl", Output: "app.json", Format: "json", Values: map[string]string{"value": "quotes \" slash \\ newline\n雪", "id": "{{ workspace.id }}"}}
	render := func(source string) (map[string][]byte, error) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, "config.tmpl"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		templates, err := generated.Load(t.Context(), directory, map[string]config.GeneratedFile{"app": definition})
		if err != nil {
			return nil, err
		}
		return templates.Render(t.Context(), func(string) (string, error) { return "workspace", nil })
	}
	files, err := render(`{"value":{{json .value}},"id":{{json .id}}}`)
	if err != nil {
		t.Fatal(err)
	}
	var received map[string]string
	if err := json.Unmarshal(files["app.json"], &received); err != nil {
		t.Fatal(err)
	}
	if received["value"] != definition.Values["value"] || received["id"] != "workspace" {
		t.Fatal("incorrect JSON escaping or reference resolution")
	}
	for name, source := range map[string]string{
		"syntax":       `{{ DO_NOT_PRINT_SECRET }}`,
		"missing key":  `DO_NOT_PRINT_SECRET {{ .missing }}`,
		"invalid JSON": `DO_NOT_PRINT_SECRET {{ .value }}`,
		"output limit": `{{printf "%1048577s" "DO_NOT_PRINT_SECRET"}}`,
		"input limit":  strings.Repeat("x", config.GeneratedFileLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			files, err := render(source)
			if err == nil || files != nil || strings.Contains(err.Error(), "DO_NOT_PRINT_SECRET") {
				t.Fatalf("rendering failure leaked data or returned a partial set: %v", err)
			}
		})
	}
	definition.Format = "text"
	files, err = render("")
	if err != nil || len(files["app.json"]) != 0 {
		t.Fatal("empty text file failed", err)
	}
	templates, err := generated.Load(t.Context(), directory, map[string]config.GeneratedFile{"app": definition})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := templates.Render(ctx, func(string) (string, error) { return "", nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTemplateConfinementAndWholeSetValidation(t *testing.T) {
	directory := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.tmpl")
	if err := os.WriteFile(outside, []byte("DO_NOT_PRINT_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "good.tmpl"), []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := config.GeneratedFile{Template: "good.tmpl", Output: "good.txt", Format: "text"}
	for _, path := range []string{outside, "../secret.tmpl", "."} {
		definition.Template = path
		if _, err := generated.Load(t.Context(), directory, map[string]config.GeneratedFile{"app": definition}); err == nil || strings.Contains(err.Error(), "DO_NOT_PRINT_SECRET") {
			t.Fatal("unsafe template accepted", err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(directory, "link")); err == nil {
		definition.Template = "link"
		if _, err := generated.Load(t.Context(), directory, map[string]config.GeneratedFile{"app": definition}); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "bad.tmpl"), []byte("{{ .missing }}"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string]config.GeneratedFile{
		"a": {Template: "good.tmpl", Output: "good.txt", Format: "text"},
		"b": {Template: "bad.tmpl", Output: "bad.txt", Format: "text"},
	}
	templates, err := generated.Load(t.Context(), directory, files)
	if err != nil {
		t.Fatal(err)
	}
	result, err := templates.Render(t.Context(), func(string) (string, error) { return "", nil })
	if err == nil || result != nil {
		t.Fatal("multi-file rendering returned partial output")
	}
}
