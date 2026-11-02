//go:build integration

package lifecycle_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

const containerFilesManifest = `version: 1
compose: [compose.yaml]
endpoints:
  api: {service: api, port: 8000, scheme: http}
bindings:
  api:
    files:
      app:
        template: app.json.tmpl
        output: config.json
        target: /run/app.json
        format: json
        values:
          url: "{{ endpoints.api.host.url }}"
          id: "{{ workspace.id }}"
      empty:
        template: empty.tmpl
        output: empty.txt
        target: /run/empty
        format: text
    environment:
      APP_CONFIG: "{{ files.app.path }}"
`

const containerFilesCompose = `services:
  api:
    image: busybox:1.37
    user: '12345:12345'
    read_only: true
    stop_grace_period: 1s
    command: [sh, -ec, 'test -r "$$APP_CONFIG"; test -f /run/empty; test ! -s /run/empty; if (echo forbidden >> "$$APP_CONFIG") 2>/dev/null; then exit 1; fi; exec httpd -f -p 8000 -h /run']
    expose: ['8000']
    healthcheck:
      test: [CMD, wget, -q, -O, /dev/null, http://127.0.0.1:8000/app.json]
      interval: 1s
      timeout: 1s
      retries: 3
`

func TestContainerGeneratedFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(main, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", containerFilesCompose)
	write("trocker.yaml", containerFilesManifest)
	write("empty.tmpl", "")
	template := `{"url":{{json .url}},"id":{{json .id}},"generation":"one"}`
	write("app.json.tmpl", template)
	before := snapshotObservationFiles(t, main, store.Directory)
	preview, err := lifecycle.Plan(ctx, main, store)
	if err != nil || preview.PlannedPorts != 1 || preview.ContainerFiles != 2 {
		t.Fatal("file-only port requirement or preview missing", err)
	}
	if !reflect.DeepEqual(before, snapshotObservationFiles(t, main, store.Directory)) {
		t.Fatal("preview wrote files")
	}
	options := compose.UpOptions{Detach: true}
	result, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	verifyContainerFile(t, ctx, result, "one")
	containers, err := target.Containers(ctx, registration)
	if err != nil || len(containers) != 1 {
		t.Fatal(err)
	}
	originalID := containers[0].ID
	inspected, err := target.Client.ContainerInspect(ctx, originalID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := ""
	for _, mount := range inspected.Container.Mounts {
		if mount.Destination == "/run/app.json" {
			if mount.RW {
				t.Fatal("generated config is writable")
			}
			source = mount.Source
		}
	}
	if source == "" {
		t.Fatal("config was not mounted")
	}
	info, err := os.Stat(source)
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatal("incorrect delivery permissions", err)
	}
	old, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	containers, err = target.Containers(ctx, registration)
	if err != nil || containers[0].ID != originalID {
		t.Fatal("unchanged files recreated service", err)
	}
	write("app.json.tmpl", strings.Replace(template, "one", "two", 1))
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Stop}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Start}, io.Discard); err != nil {
		t.Fatal(err)
	}
	observed, err := lifecycle.Endpoints(ctx, main, store)
	if err != nil {
		t.Fatal(err)
	}
	verifyContainerFile(t, ctx, observed, "one")
	result, err = lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	verifyContainerFile(t, ctx, result, "two")
	containers, err = target.Containers(ctx, registration)
	if err != nil || containers[0].ID == originalID {
		t.Fatal("changed content did not recreate service", err)
	}
	retained, err := os.ReadFile(source)
	if err != nil || string(retained) != string(old) {
		t.Fatal("previous generation changed", err)
	}
	write("app.json.tmpl", "DO_NOT_PRINT_SECRET {{.missing}}")
	_, err = lifecycle.Up(ctx, main, store, options, io.Discard)
	if err == nil || strings.Contains(err.Error(), "DO_NOT_PRINT_SECRET") {
		t.Fatal("invalid rendering succeeded or leaked content", err)
	}
	verifyContainerFile(t, ctx, result, "two")
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down, Volumes: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("down removed generated files", err)
	}
}

func verifyContainerFile(t *testing.T, ctx context.Context, result lifecycle.EndpointResult, generation string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, result.Endpoints[0].Addresses.Host.URL+"/app.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]string
	decodeErr := json.NewDecoder(response.Body).Decode(&document)
	closeErr := response.Body.Close()
	if decodeErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
		t.Fatal("cannot read generated JSON", decodeErr, closeErr)
	}
	if document["url"] != result.Endpoints[0].Addresses.Host.URL || document["id"] != result.WorkspaceID || document["generation"] != generation {
		t.Fatal("incorrect generated configuration", document)
	}
}
