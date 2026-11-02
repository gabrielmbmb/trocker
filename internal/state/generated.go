package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/gabrielmbmb/trocker/internal/config"
)

type generatedIndex struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
}

// PublishGenerated publishes a complete immutable native file set. A consumer-
// scoped lock protects only comparison/publication, never rendering or execution.
// Old and orphaned complete generations are deliberately retained.
func (s Store) PublishGenerated(ctx context.Context, workspaceID, targetID, service string, files map[string][]byte) (paths map[string]string, err error) {
	return s.publishGenerated(ctx, workspaceID, targetID, service, "native", files)
}

// PublishContainerFiles keeps files inside private directories while permitting
// arbitrary container UIDs to read their individually read-only mounted files.
func (s Store) PublishContainerFiles(ctx context.Context, workspaceID, targetID, service string, files map[string][]byte) (map[string]string, error) {
	if targetID == "" {
		return nil, fmt.Errorf("container files require a verified target")
	}
	return s.publishGenerated(ctx, workspaceID, targetID, service, "container", files)
}

func (s Store) publishGenerated(ctx context.Context, workspaceID, targetID, service, mode string, files map[string][]byte) (paths map[string]string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	if !canonicalUUID(workspaceID) || service == "" {
		return nil, fmt.Errorf("invalid generated-file consumer identity")
	}
	names := make(map[string]bool)
	for name, data := range files {
		folded := strings.ToLower(name)
		if !config.GeneratedFilename(name) || names[folded] || len(data) > config.GeneratedFileLimit {
			return nil, fmt.Errorf("invalid generated file set")
		}
		names[folded] = true
	}
	scope := "local"
	if targetID != "" {
		scope = fmt.Sprintf("target-%x", sha256.Sum256([]byte(targetID)))
	}
	relative := filepath.Join("generated", workspaceID, scope, mode, fmt.Sprintf("service-%x", sha256.Sum256([]byte(service))))
	defer func() {
		if err != nil {
			paths = nil
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				err = &generatedStateError{cause: err}
			}
		}
	}()
	lock, err := s.lock(ctx, fmt.Sprintf("generated-%x", sha256.Sum256([]byte(relative))))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	root, err := s.generatedRoot(relative)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	permission := os.FileMode(0o600)
	if mode == "container" {
		permission = 0o444
	}
	generation, err := matchingGeneration(root, files, permission)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if generation == "" {
		generation, err = publishGeneration(ctx, root, files, permission)
		if err != nil {
			return nil, err
		}
	}
	paths = make(map[string]string, len(files))
	for name := range files {
		paths[name] = filepath.Join(root.Name(), generation, name)
	}
	return paths, nil
}

func canonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func (s Store) generatedRoot(relative string) (*os.Root, error) {
	directory, err := filepath.Abs(s.Directory)
	if err != nil {
		return nil, err
	}
	if err := privatePath(directory, true); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	for component := range strings.SplitSeq(relative, string(filepath.Separator)) {
		if err := root.Mkdir(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, errors.Join(err, root.Close())
		}
		if err := privateEntry(root, component, true); err != nil {
			return nil, errors.Join(err, root.Close())
		}
		next, openErr := root.OpenRoot(component)
		closeErr := root.Close()
		if openErr != nil {
			return nil, errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			return nil, errors.Join(closeErr, next.Close())
		}
		root = next
	}
	return root, nil
}

func privateEntry(root *os.Root, name string, directory bool) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("generated state must not contain symlinks or special files")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("generated state permissions are not private")
	}
	return nil
}

func matchingGeneration(root *os.Root, files map[string][]byte, permission os.FileMode) (generation string, err error) {
	if err := privateEntry(root, "current.json", false); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	data, err := boundedStateRead(root, "current.json", 1024)
	if err != nil {
		return "", err
	}
	var index generatedIndex
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("invalid generation index")
	}
	if index.Version != 1 || !canonicalUUID(index.Generation) {
		return "", fmt.Errorf("invalid generation index")
	}
	if err := privateEntry(root, index.Generation, true); err != nil {
		return "", err
	}
	current, err := root.OpenRoot(index.Generation)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, current.Close()) }()
	entries, err := fs.ReadDir(current.FS(), ".")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		info, err := current.Lstat(entry.Name())
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != permission {
			return "", fmt.Errorf("unsafe generated file permissions or type")
		}
	}
	if len(entries) != len(files) {
		return "", nil
	}
	for _, entry := range entries {
		expected, ok := files[entry.Name()]
		if !ok {
			return "", nil
		}
		actual, err := boundedStateRead(current, entry.Name(), config.GeneratedFileLimit)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(expected, actual) {
			return "", nil
		}
	}
	return index.Generation, nil
}

func boundedStateRead(root *os.Root, name string, limit int64) (data []byte, err error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("invalid generated state file")
	}
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("generated state file exceeds size limit")
	}
	return data, nil
}

func publishGeneration(ctx context.Context, root *os.Root, files map[string][]byte, permission os.FileMode) (generation string, err error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	generation = id.String()
	staging := ".staging-" + generation
	if err := root.Mkdir(staging, 0o700); err != nil {
		return "", err
	}
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, root.RemoveAll(staging))
		}
	}()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := writeGenerated(root, filepath.Join(staging, name), files[name], permission); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := root.Rename(staging, generation); err != nil {
		return "", err
	}
	published = true
	if err := writeGenerationIndex(ctx, root, generation); err != nil {
		return "", err
	}
	return generation, nil
}

func writeGenerated(root *os.Root, name string, data []byte, permission os.FileMode) (err error) {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			err = errors.Join(err, root.Remove(name))
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	// Set final permissions through the descriptor, independent of umask.
	if err := file.Chmod(permission); err != nil {
		return err
	}
	return file.Sync()
}

func writeGenerationIndex(ctx context.Context, root *os.Root, generation string) (err error) {
	data, err := json.Marshal(generatedIndex{Version: 1, Generation: generation})
	if err != nil {
		return err
	}
	temporary := ".current-" + generation + ".tmp"
	if err := writeGenerated(root, temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, root.Remove(temporary))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return root.Rename(temporary, "current.json")
}

type generatedStateError struct{ cause error }

func (*generatedStateError) Error() string { return "cannot publish generated files" }
func (*generatedStateError) Help() string {
	return "inspect the workspace's private generated state and its permissions; existing generations were left unchanged"
}
func (e *generatedStateError) Unwrap() error { return e.cause }
