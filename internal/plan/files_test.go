package plan_test

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestContainerFileSelectionAndMountSafety(t *testing.T) {
	_, path := fixture(t, "services:\n  api: {image: busybox, expose: [8000]}\n  web: {image: busybox}\n")
	manifest, source, err := compose.LoadForPlan(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Endpoints = map[string]config.Endpoint{"api": {Service: "api", Port: 8000, Scheme: "http"}}
	file := config.GeneratedFile{Template: "app.tmpl", Output: "app.json", Target: "/run/app.json", Format: "json", Values: map[string]string{"url": "{{ endpoints.api.host.url }}"}}
	manifest.Bindings = map[string]config.Bindings{"web": {Files: map[string]config.GeneratedFile{"app": file}, Environment: map[string]string{"APP_CONFIG": "{{ files.app.path }}"}}}
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	if _, err := plan.Build(manifest, source, registration, plan.Options{Services: []string{"web"}, Preview: true}); err == nil {
		t.Fatal("file dependency outside selection accepted")
	}
	project, err := plan.Build(manifest, source, registration, plan.Options{Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	required, err := plan.RequiredPorts(manifest, project)
	if err != nil || len(required) != 1 || required[0].Service != "api" {
		t.Fatal("missing file-only allocation requirement", err)
	}
	if *project.Services["web"].Environment["APP_CONFIG"] != "/run/app.json" {
		t.Fatal("container received host source path")
	}
	for _, mount := range []string{"/run", "/run/app.json", "/run/app.json/nested", "/"} {
		service := source.Services["web"]
		service.Volumes = []types.ServiceVolumeConfig{{Type: types.VolumeTypeTmpfs, Target: mount}}
		source.Services["web"] = service
		if _, err := plan.Build(manifest, source, registration, plan.Options{Preview: true}); err == nil {
			t.Fatal("mount overlap accepted", mount)
		}
	}
	service := source.Services["web"]
	service.Volumes = nil
	service.Tmpfs = []string{"/run:size=64m"}
	source.Services["web"] = service
	if _, err := plan.Build(manifest, source, registration, plan.Options{Preview: true}); err == nil {
		t.Fatal("tmpfs overlap accepted")
	}
	service.Tmpfs = nil
	source.Services["web"] = service
	for _, target := range []string{"", "/", "relative", "/run/../app", "/proc/app", "/sys/app", "/dev/app", "/etc/hosts", "/etc/resolv.conf"} {
		file.Target = target
		manifest.Bindings["web"].Files["app"] = file
		if _, err := plan.Build(manifest, source, registration, plan.Options{Preview: true}); err == nil {
			t.Fatal("unsafe target accepted", target)
		}
	}
	file.Target = "/run/app.json"
	manifest.Bindings["web"].Files["app"] = file
	second := file
	second.Output = "second.json"
	second.Target = "/run/app.json/nested"
	manifest.Bindings["web"].Files["second"] = second
	if _, err := plan.Build(manifest, source, registration, plan.Options{Preview: true}); err == nil {
		t.Fatal("overlapping generated targets accepted")
	}
}
