package config

import (
	"io/fs"
	"path/filepath"
	"strings"
)

const GeneratedFileLimit = 1 << 20

// GeneratedFilename excludes path syntax on all supported hosts, not just the
// current OS. Files live inside a generation; users never choose its directory.
func GeneratedFilename(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.IsLocal(name) && !strings.ContainsAny(name, "/\\:\x00")
}

func (m *Manifest) validateFiles(files map[string]GeneratedFile, environment map[string]string, field string, isNative bool) error {
	used := make(map[string]bool)
	for _, value := range environment {
		_, err := Expand(value, func(reference string) (string, error) {
			if strings.HasPrefix(reference, "files.") {
				used[strings.Split(reference, ".")[1]] = true
			}
			return "", nil
		})
		if err != nil {
			return err
		}
	}
	outputs := make(map[string]bool)
	for name, file := range files {
		if !aliasPattern.MatchString(name) {
			return invalid(field+".files", "invalid file identifier")
		}
		path := field + ".files." + name
		if !fs.ValidPath(file.Template) || file.Template == "." || strings.ContainsAny(file.Template, "\\:\x00") {
			return invalid(path+".template", "expected a relative template path inside the manifest directory")
		}
		if !GeneratedFilename(file.Output) {
			return invalid(path+".output", "expected a filename, not a path")
		}
		key := strings.ToLower(file.Output)
		if outputs[key] {
			return invalid(path+".output", "duplicate output filename")
		}
		outputs[key] = true
		if file.Format != "json" && file.Format != "text" {
			return invalid(path+".format", "expected json or text")
		}
		if isNative && file.Target != "" {
			return invalid(path+".target", "native files cannot set a container target")
		}
		if !isNative && !ValidFileTarget(file.Target) {
			return invalid(path+".target", "expected a clean absolute file path outside special filesystems")
		}
		if isNative && !used[name] {
			return invalid(path, "native file needs a files reference in its native environment")
		}
		for key, value := range file.Values {
			if !environmentPattern.MatchString(key) {
				return invalid(path+".values", "invalid template value name")
			}
			if err := m.validateValue(value, path+".values."+key, isNative, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
