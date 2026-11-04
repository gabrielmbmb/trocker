package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"

	"github.com/gabrielmbmb/trocker/internal/config"
)

type generatedIndex struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
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
