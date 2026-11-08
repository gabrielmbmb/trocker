package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// EnvironmentMarker tells a Trocker process that the environment wrapper
// started it. Its value is the wrapper fingerprint, so a script edited while a
// command runs is not mistaken for the one that was approved.
const EnvironmentMarker = "TROCKER_ENVIRONMENT_WRAPPER"

// EnvironmentWrapper is a checkout's environment_wrapper: a script that adds
// environment, such as secrets, and then runs Trocker. It runs only once a
// user approved its fingerprint through init.
type EnvironmentWrapper struct {
	// Command is the command as declared, relative to the manifest.
	Command     []string
	Path        string
	Fingerprint string
	Approved    bool
}

// Active reports whether this process was started by this exact wrapper.
func (w *EnvironmentWrapper) Active() bool {
	return w != nil && os.Getenv(EnvironmentMarker) == w.Fingerprint
}

// LoadEnvironmentWrapper returns the wrapper the checkout's manifest declares,
// or nil when there is none. A directory without a checkout or a valid manifest
// also returns nil: the command that runs next reports that problem itself.
func LoadEnvironmentWrapper(ctx context.Context, directory string, store state.Store) (*EnvironmentWrapper, error) {
	checkout, err := workspace.Discover(ctx, directory)
	if err != nil {
		return nil, nil //nolint:nilerr // The command reports a missing checkout.
	}
	manifest, err := config.Load(filepath.Join(checkout.Root, "trocker.yaml"))
	if err != nil {
		return nil, nil //nolint:nilerr // The command reports an invalid manifest.
	}
	wrapper, err := resolveEnvironmentWrapper(manifest)
	if err != nil || wrapper == nil {
		return nil, err
	}
	registry, err := store.Read()
	if err != nil {
		return nil, err
	}
	wrapper.Approved = registry.WrapperApproved(wrapper.Fingerprint)
	return wrapper, nil
}

func resolveEnvironmentWrapper(manifest *config.Manifest) (*EnvironmentWrapper, error) {
	if len(manifest.EnvironmentWrapper) == 0 {
		return nil, nil
	}
	path := manifest.EnvironmentWrapperPath()
	script, err := os.ReadFile(path) //nolint:gosec // The manifest confines the script to the checkout.
	if err != nil {
		return nil, &wrapperScriptError{script: manifest.EnvironmentWrapper[0], cause: err}
	}
	return &EnvironmentWrapper{
		Command:     manifest.EnvironmentWrapper,
		Path:        path,
		Fingerprint: wrapperFingerprint(manifest.EnvironmentWrapper, script),
	}, nil
}

// wrapperFingerprint covers the declared command and the script's content but
// not the checkout path, so one approval applies to every worktree using the
// same wrapper. Files the script reads are not covered.
func wrapperFingerprint(command []string, script []byte) string {
	var canonical strings.Builder
	write := func(value string) {
		canonical.WriteString(strconv.Itoa(len(value)))
		canonical.WriteByte(':')
		canonical.WriteString(value)
	}
	write(strconv.Itoa(len(command)))
	for _, part := range command {
		write(part)
	}
	write(string(script))
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

type wrapperScriptError struct {
	script string
	cause  error
}

func (e *wrapperScriptError) Error() string {
	return "cannot read environment wrapper " + e.script
}

func (*wrapperScriptError) Help() string {
	return "check environment_wrapper in trocker.yaml"
}

func (e *wrapperScriptError) Unwrap() error { return e.cause }

// deferredComposeError guards registration: Compose has not been validated
// while an unapproved wrapper has not run.
type deferredComposeError struct{ command []string }

func (e *deferredComposeError) Error() string {
	return fmt.Sprintf("Compose cannot be validated before environment wrapper %s runs", strings.Join(e.command, " "))
}

func (*deferredComposeError) Help() string {
	return "approve the wrapper with trocker init, which validates Compose inside it"
}
