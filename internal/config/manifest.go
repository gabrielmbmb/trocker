// Package config loads Trocker's explicit configuration, not Compose syntax.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

type Manifest struct {
	Version              int                        `yaml:"version"`
	Compose              []string                   `yaml:"compose"`
	ReadWritePaths       []string                   `yaml:"read_write_paths"`
	SharedWritePaths     []string                   `yaml:"shared_write_paths"`
	BuildContexts        []string                   `yaml:"build_contexts"`
	DockerSocketServices []string                   `yaml:"docker_socket_services"`
	HostAccess           map[string]HostAccessGrant `yaml:"host_access"`
	Endpoints            map[string]Endpoint        `yaml:"endpoints"`
	Bindings             map[string]Bindings        `yaml:"bindings"`
	Directory            string                     `yaml:"-"`
}

type Endpoint struct {
	Service string `yaml:"service"`
	Port    uint16 `yaml:"port"`
	Scheme  string `yaml:"scheme"`
}

type Bindings struct {
	Environment map[string]string        `yaml:"environment"`
	Native      NativeBindings           `yaml:"native"`
	Files       map[string]GeneratedFile `yaml:"files"`
}

type NativeBindings struct {
	Environment map[string]string        `yaml:"environment"`
	Files       map[string]GeneratedFile `yaml:"files"`
}

type GeneratedFile struct {
	Template string            `yaml:"template"`
	Target   string            `yaml:"target"`
	Output   string            `yaml:"output"`
	Format   string            `yaml:"format"`
	Values   map[string]string `yaml:"values"`
}

// Error omits rejected values and raw parser messages, which may contain secrets.
// The cause is retained for classification, not for presentation.
type Error struct {
	Field   string
	Problem string
	cause   error
}

func (e *Error) Error() string { return e.Field + ": " + e.Problem }
func (e *Error) Unwrap() error { return e.cause }

func invalid(field, problem string) error { return &Error{Field: field, Problem: problem} }

var (
	aliasPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	environmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Load reads exactly the supplied manifest and resolves its Compose paths.
// Workspace discovery and loading the referenced Compose files happen separately.
func Load(path string) (*Manifest, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, &Error{Field: "manifest", Problem: "cannot resolve path", cause: err}
	}
	data, err := os.ReadFile(absolute) //nolint:gosec // Loading the caller-selected manifest is intentional.
	if err != nil {
		return nil, &Error{Field: "manifest", Problem: "cannot read file", cause: err}
	}
	manifest, err := Parse(data)
	if err != nil {
		return nil, err
	}
	manifest.Directory = filepath.Dir(absolute)
	for i, path := range manifest.Compose {
		if !filepath.IsAbs(path) {
			path = filepath.Join(manifest.Directory, path)
		}
		manifest.Compose[i] = filepath.Clean(path)
	}
	if manifest.ReadWritePaths, err = manifest.resolveCheckoutPaths("read_write_paths", manifest.ReadWritePaths); err != nil {
		return nil, err
	}
	if manifest.BuildContexts, err = manifest.resolveCheckoutPaths("build_contexts", manifest.BuildContexts); err != nil {
		return nil, err
	}
	if manifest.SharedWritePaths, err = manifest.resolveSharedWritePaths(); err != nil {
		return nil, err
	}
	return manifest, nil
}

// resolveCheckoutPaths makes declared host paths absolute but never lets them
// reach outside the checkout. Containment is lexical: a source is matched
// against the declared path, not against the symlinks it may resolve to.
func (m *Manifest) resolveCheckoutPaths(field string, paths []string) ([]string, error) {
	resolved := slices.Clone(paths)
	for i, path := range resolved {
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.Directory, path)
		}
		path = filepath.Clean(path)
		if !Inside(m.Directory, path) {
			return nil, invalid(fmt.Sprintf("%s[%d]", field, i), "expected a path inside the manifest directory")
		}
		resolved[i] = path
	}
	return resolved, nil
}

// resolveSharedWritePaths expands ~ and rejects paths no mount may write.
// Host paths outside the checkout belong to the machine, not to Trocker, so
// this does not inspect the filesystem: read-only commands must not depend on
// the host's state, and the mount itself fails when the directory is missing.
func (m *Manifest) resolveSharedWritePaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, &Error{Field: "shared_write_paths", Problem: "cannot resolve the home directory", cause: err}
	}
	resolved := make([]string, len(m.SharedWritePaths))
	seen := make(map[string]bool, len(resolved))
	for i, path := range m.SharedWritePaths {
		field := fmt.Sprintf("shared_write_paths[%d]", i)
		path = strings.TrimSpace(path)
		if path == "~" || strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		} else if strings.HasPrefix(path, "~") {
			return nil, invalid(field, "expected ~/ or an absolute path; named home directories are not supported")
		}
		if !filepath.IsAbs(path) {
			return nil, invalid(field, "expected ~/ or an absolute path outside the checkout")
		}
		path = filepath.Clean(path)
		switch {
		case path == filepath.Dir(path):
			return nil, invalid(field, "expected a path below the filesystem root")
		case path == home:
			return nil, invalid(field, "expected a path below the home directory, not the home directory itself")
		case Inside(m.Directory, path):
			return nil, invalid(field, "expected a path outside the checkout; use read_write_paths for checkout paths")
		case Inside(path, m.Directory):
			return nil, invalid(field, "expected a path that does not contain the checkout")
		case seen[path]:
			return nil, invalid("shared_write_paths", "duplicate path "+path)
		}
		seen[path] = true
		resolved[i] = path
	}
	return resolved, nil
}

// Inside reports whether path is the manifest directory or below it.
func (m *Manifest) Inside(path string) bool { return Inside(m.Directory, path) }

// AllowsWrite reports whether a bind mount source may be mounted writable.
// Callers must pass a clean absolute path, which Compose produces.
func (m *Manifest) AllowsWrite(source string) bool { return allows(m.ReadWritePaths, source) }

// AllowsSharedWrite reports whether a writable mount may use a host path
// outside the checkout. Callers must pass a clean absolute path.
func (m *Manifest) AllowsSharedWrite(source string) bool { return allows(m.SharedWritePaths, source) }

func allows(paths []string, source string) bool {
	for _, path := range paths {
		if Inside(path, source) {
			return true
		}
	}
	return false
}

// AllowsBuildContext reports whether an image build may read a host path.
// Callers must pass a clean absolute path, which Compose produces.
func (m *Manifest) AllowsBuildContext(source string) bool { return allows(m.BuildContexts, source) }

// AllowsDockerSocket reports whether service may access the Docker API socket.
func (m *Manifest) AllowsDockerSocket(service string) bool {
	return slices.Contains(m.DockerSocketServices, service)
}

// Inside reports whether path is root or a descendant of root. Rel compares
// path components, so a sibling sharing a name prefix is not contained.
func Inside(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Parse delegates YAML structure and type conversion to the library, then
// validates Trocker's rules. It does not resolve paths or access external state.
func Parse(data []byte) (*Manifest, error) {
	var manifest Manifest
	if err := yaml.Load(data, &manifest, yaml.WithKnownFields(), yaml.WithUniqueKeys()); err != nil {
		field := "manifest"
		var loadErr *yaml.LoadError
		if errors.As(err, &loadErr) && loadErr.Mark.Line > 0 {
			field = fmt.Sprintf("manifest (line %d, column %d)", loadErr.Mark.Line, loadErr.Mark.Column)
		}
		return nil, &Error{Field: field, Problem: "invalid YAML structure or field type; check keys, types, and document count", cause: err}
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// Validate checks manifest semantics without a Compose project or runtime state.
func (m *Manifest) Validate() error {
	if m.Version != 1 {
		return invalid("version", "required version is 1")
	}
	if len(m.Compose) == 0 {
		return invalid("compose", "expected a nonempty list of file paths")
	}
	for i, path := range m.Compose {
		if strings.TrimSpace(path) == "" {
			return invalid(fmt.Sprintf("compose[%d]", i), "expected a nonempty file path")
		}
	}
	for i, path := range m.ReadWritePaths {
		if strings.TrimSpace(path) == "" {
			return invalid(fmt.Sprintf("read_write_paths[%d]", i), "expected a nonempty host path")
		}
	}
	for i, path := range m.BuildContexts {
		if strings.TrimSpace(path) == "" {
			return invalid(fmt.Sprintf("build_contexts[%d]", i), "expected a nonempty host path")
		}
	}
	for i, path := range m.SharedWritePaths {
		if strings.TrimSpace(path) == "" {
			return invalid(fmt.Sprintf("shared_write_paths[%d]", i), "expected a nonempty host path")
		}
	}
	seen := make(map[string]bool, len(m.DockerSocketServices))
	for i, service := range m.DockerSocketServices {
		if strings.TrimSpace(service) == "" {
			return invalid(fmt.Sprintf("docker_socket_services[%d]", i), "expected a nonempty service name")
		}
		if seen[service] {
			return invalid("docker_socket_services", "duplicate service "+service)
		}
		seen[service] = true
	}
	if err := m.validateHostAccess(); err != nil {
		return err
	}
	for name, endpoint := range m.Endpoints {
		if !aliasPattern.MatchString(name) {
			return invalid("endpoints", "invalid endpoint alias")
		}
		field := "endpoints." + name
		if strings.TrimSpace(endpoint.Service) == "" {
			return invalid(field+".service", "expected a nonempty service name")
		}
		if endpoint.Port == 0 {
			return invalid(field+".port", "expected a port from 1 through 65535")
		}
		switch endpoint.Scheme {
		case "http", "https", "ws", "wss", "tcp":
		default:
			return invalid(field+".scheme", "missing or unsupported scheme")
		}
	}
	return m.validateBindings()
}
