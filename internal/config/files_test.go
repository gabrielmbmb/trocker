package config_test

import (
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/config"
)

const fileManifest = `version: 1
compose: [compose.yaml]
endpoints:
  api: {service: api, port: 8000, scheme: http}
bindings:
  app:
    native:
      files:
        app_config:
          template: .trocker/app.json.tmpl
          output: app.json
          format: json
          values:
            api_url: "{{ endpoints.api.host.url }}"
      environment:
        APP_CONFIG: "{{ files.app_config.path }}"
`

func TestNativeFileDeclarations(t *testing.T) {
	if _, err := config.Parse([]byte(fileManifest)); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"absolute input":             strings.Replace(fileManifest, ".trocker/app.json.tmpl", "/outside/template", 1),
		"traversal input":            strings.Replace(fileManifest, ".trocker/app.json.tmpl", "../outside/template", 1),
		"absolute output":            strings.Replace(fileManifest, "output: app.json", "output: /outside.json", 1),
		"nested output":              strings.Replace(fileManifest, "output: app.json", "output: dir/app.json", 1),
		"windows output":             strings.Replace(fileManifest, "output: app.json", `output: 'C:\outside.json'`, 1),
		"format":                     strings.Replace(fileManifest, "format: json", "format: yaml", 1),
		"unconsumed":                 strings.Replace(fileManifest, "{{ files.app_config.path }}", "unused", 1),
		"unknown file":               strings.Replace(fileManifest, "files.app_config.path", "files.missing.path", 1),
		"unknown path component":     strings.Replace(fileManifest, "files.app_config.path", "files.app_config.contents", 1),
		"file dependency":            strings.Replace(fileManifest, "endpoints.api.host.url", "files.app_config.path", 1),
		"native container reference": strings.Replace(fileManifest, "endpoints.api.host.url", "endpoints.api.container.url", 1),
		"native mount":               strings.Replace(fileManifest, "output: app.json", "output: app.json\n          target: /run/app.json", 1),
		"unknown file field":         strings.Replace(fileManifest, "output: app.json", "output: app.json\n          secret_field: DO_NOT_PRINT_SECRET", 1),
		"container file":             strings.Replace(fileManifest, "    native:\n", "", 1),
		"container path":             strings.Replace(fileManifest, "    native:\n", "    environment: {APP_CONFIG: '{{ files.app_config.path }}'}\n    native:\n", 1),
		"duplicate output":           strings.Replace(fileManifest, "      environment:", "        second:\n          template: second.tmpl\n          output: APP.JSON\n          format: text\n      environment:\n        SECOND: '{{ files.second.path }}'", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse([]byte(input))
			if err == nil || strings.Contains(err.Error(), "DO_NOT_PRINT_SECRET") {
				t.Fatalf("invalid declaration accepted or leaked values: %v", err)
			}
		})
	}
}
