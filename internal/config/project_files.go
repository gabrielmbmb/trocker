package config

import (
	"fmt"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

func validateProjectFiles(files map[string]GeneratedFile, service types.ServiceConfig) error {
	targets := make([]string, 0, len(files))
	for _, mount := range service.Volumes {
		targets = append(targets, mount.Target)
	}
	for _, mount := range service.Tmpfs {
		target, _, _ := strings.Cut(mount, ":")
		targets = append(targets, target)
	}
	for _, mount := range service.Configs {
		target := mount.Target
		if target == "" {
			target = mount.Source
		}
		if !strings.HasPrefix(target, "/") {
			target = "/" + target
		}
		targets = append(targets, target)
	}
	for _, mount := range service.Secrets {
		target := mount.Target
		if target == "" {
			target = mount.Source
		}
		if !strings.HasPrefix(target, "/") {
			target = "/run/secrets/" + target
		}
		targets = append(targets, target)
	}
	for _, file := range files {
		for _, target := range targets {
			if PathsOverlap(file.Target, target) {
				return fmt.Errorf("generated file targets overlap each other or a Compose mount")
			}
		}
		targets = append(targets, file.Target)
	}
	return nil
}
