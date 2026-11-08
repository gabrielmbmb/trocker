// Package config loads Trocker's explicit configuration, not Compose syntax.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

type Manifest struct {
	Version              int                        `yaml:"version"`
	Compose              []string                   `yaml:"compose"`
	Profiles             []string                   `yaml:"profiles"`
	ReadWritePaths       []string                   `yaml:"read_write_paths"`
	SharedWritePaths     []string                   `yaml:"shared_write_paths"`
	BuildContexts        []string                   `yaml:"build_contexts"`
	DockerSocketServices []string                   `yaml:"docker_socket_services"`
	HostAccess           map[string]HostAccessGrant `yaml:"host_access"`
	StablePorts          bool                       `yaml:"stable_ports"`
	StablePortRange      string                     `yaml:"stable_port_range"`
	EnvironmentWrapper   []string                   `yaml:"environment_wrapper"`
	Endpoints            map[string]Endpoint        `yaml:"endpoints"`
	Bindings             map[string]Bindings        `yaml:"bindings"`
	Directory            string                     `yaml:"-"`
}

// DefaultStablePortRange ends below 32768, where Linux's default ephemeral
// range begins, so on Linux, macOS, and Windows defaults it never contains a
// port the OS hands to another program's dynamic listener.
var DefaultStablePortRange = PortRange{First: 20000, Last: 32767}

// lowestStablePort keeps assignments out of the well-known ports, which system
// services occupy and rootless Docker cannot publish without extra privileges.
const lowestStablePort = 1024

// PortRange is an inclusive range of TCP ports.
type PortRange struct {
	First uint16
	Last  uint16
}

func (r PortRange) Contains(port uint16) bool { return port >= r.First && port <= r.Last }

func (r PortRange) Overlaps(other PortRange) bool {
	return r.First <= other.Last && other.First <= r.Last
}

func (r PortRange) String() string { return fmt.Sprintf("%d-%d", r.First, r.Last) }

// ParsePortRange reads an inclusive first-last range such as 20000-32767.
func ParsePortRange(value string) (PortRange, bool) {
	first, last, found := strings.Cut(value, "-")
	if !found {
		return PortRange{}, false
	}
	start, startErr := strconv.ParseUint(first, 10, 16)
	end, endErr := strconv.ParseUint(last, 10, 16)
	if startErr != nil || endErr != nil || start < lowestStablePort || start > end {
		return PortRange{}, false
	}
	return PortRange{First: uint16(start), Last: uint16(end)}, true
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
	profilePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
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
	if len(manifest.EnvironmentWrapper) > 0 && !Inside(manifest.Directory, manifest.EnvironmentWrapperPath()) {
		return nil, invalid("environment_wrapper[0]", "expected a script inside the manifest directory")
	}
	return manifest, nil
}

// EnvironmentWrapperPath returns the wrapper script's absolute path. The
// declared command stays relative, so it reads the same in every worktree.
func (m *Manifest) EnvironmentWrapperPath() string {
	if len(m.EnvironmentWrapper) == 0 {
		return ""
	}
	return filepath.Join(m.Directory, filepath.Clean(m.EnvironmentWrapper[0]))
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

// StablePortPool returns the declared stable_port_range, or the default.
func (m *Manifest) StablePortPool() PortRange {
	if pool, ok := ParsePortRange(m.StablePortRange); ok {
		return pool
	}
	return DefaultStablePortRange
}

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
	if err := m.validateProfiles(); err != nil {
		return err
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
	if err := m.validateStablePortRange(); err != nil {
		return err
	}
	if len(m.EnvironmentWrapper) > 0 {
		script := m.EnvironmentWrapper[0]
		if strings.TrimSpace(script) == "" || filepath.IsAbs(script) || strings.HasPrefix(script, "~") {
			return invalid("environment_wrapper[0]", "expected a script path relative to the manifest directory")
		}
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

func (m *Manifest) validateStablePortRange() error {
	if m.StablePortRange == "" {
		return nil
	}
	if !m.StablePorts {
		return invalid("stable_port_range", "requires stable_ports: true")
	}
	if _, ok := ParsePortRange(m.StablePortRange); !ok {
		return invalid("stable_port_range", fmt.Sprintf("expected first-last, from %d through 65535, with first not above last", lowestStablePort))
	}
	return nil
}

// validateProfiles checks names and duplicates. A nonempty selection replaces
// the SDK's COMPOSE_PROFILES fallback.
func (m *Manifest) validateProfiles() error {
	seen := make(map[string]bool, len(m.Profiles))
	for i, profile := range m.Profiles {
		if profile != "*" && !profilePattern.MatchString(profile) {
			return invalid(fmt.Sprintf("profiles[%d]", i), "expected a Compose profile name or *")
		}
		if seen[profile] {
			return invalid("profiles", "duplicate profile "+profile)
		}
		seen[profile] = true
	}
	return nil
}

// ProfileSummary formats profile names for diagnostics. Empty values are
// ignored, duplicates are removed, and names are sorted.
func ProfileSummary(profiles []string) string {
	named := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if profile != "" {
			named = append(named, profile)
		}
	}
	if len(named) == 0 {
		return "none"
	}
	slices.Sort(named)
	return strings.Join(slices.Compact(named), ", ")
}
