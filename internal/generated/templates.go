// Package generated renders explicit application configuration, never source files.
package generated

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"text/template"

	"github.com/gabrielmbmb/trocker/internal/config"
)

type (
	compiledFile struct {
		definition config.GeneratedFile
		template   *template.Template
	}
	Templates struct{ files map[string]compiledFile }
	Counts    struct{ Native, Container int }
)

// Validate checks inputs and template syntax without resolving values or writing.
func Validate(ctx context.Context, manifest *config.Manifest) (Counts, error) {
	count := Counts{}
	for _, name := range slices.Sorted(maps.Keys(manifest.Bindings)) {
		files := manifest.Bindings[name].Native.Files
		if _, err := Load(ctx, manifest.Directory, files); err != nil {
			return Counts{}, fmt.Errorf("native files for service %s: %w", name, err)
		}
		count.Native += len(files)
		if _, err := Load(ctx, manifest.Directory, manifest.Bindings[name].Files); err != nil {
			return Counts{}, fmt.Errorf("container files for service %s: %w", name, err)
		}
		count.Container += len(manifest.Bindings[name].Files)
	}
	return count, nil
}

func Load(ctx context.Context, directory string, files map[string]config.GeneratedFile) (result *Templates, err error) {
	result = &Templates{files: make(map[string]compiledFile, len(files))}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return result, nil
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, &fileError{problem: "cannot open manifest directory", cause: err}
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		definition := files[name]
		data, err := readTemplate(root, definition.Template)
		if err != nil {
			return nil, &fileError{name: name, problem: "cannot read a regular template within the manifest directory (maximum 1 MiB)", cause: err}
		}
		parsed, err := template.New(name).Option("missingkey=error").Funcs(template.FuncMap{"json": encodeJSON}).Parse(string(data))
		if err != nil {
			return nil, &fileError{name: name, problem: "invalid template syntax", cause: err}
		}
		result.files[name] = compiledFile{definition: definition, template: parsed}
	}
	return result, nil
}

func readTemplate(root *os.Root, path string) (data []byte, err error) {
	// Check before opening to reject ordinary FIFO/device inputs without blocking;
	// recheck the opened descriptor too. Root confines all symlink resolution.
	info, err := root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > config.GeneratedFileLimit {
		return nil, fmt.Errorf("not a bounded regular file")
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	data, err = io.ReadAll(io.LimitReader(file, config.GeneratedFileLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > config.GeneratedFileLimit {
		return nil, fmt.Errorf("template exceeds size limit")
	}
	return data, nil
}

// Render returns the complete set keyed by output filename. No partial set is
// returned on failure, and neither raw template errors nor values are printed.
func (t *Templates) Render(ctx context.Context, resolve func(string) (string, error)) (map[string][]byte, error) {
	result := make(map[string][]byte, len(t.files))
	for _, name := range slices.Sorted(maps.Keys(t.files)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file := t.files[name]
		values := make(map[string]string, len(file.definition.Values))
		for key, value := range file.definition.Values {
			resolved, err := config.Expand(value, resolve)
			if err != nil {
				return nil, err
			}
			values[key] = resolved
		}
		output := boundedOutput{ctx: ctx}
		if err := file.template.Execute(&output, values); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &fileError{name: name, problem: "rendering failed; check template keys and the 1 MiB output limit", cause: err}
		}
		if file.definition.Format == "json" && !json.Valid(output.buffer.Bytes()) {
			return nil, &fileError{name: name, problem: "rendered output is not valid JSON"}
		}
		result[file.definition.Output] = output.buffer.Bytes()
	}
	return result, nil
}

func encodeJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

type boundedOutput struct {
	buffer bytes.Buffer
	ctx    context.Context
}

func (w *boundedOutput) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) > config.GeneratedFileLimit-w.buffer.Len() {
		return 0, fmt.Errorf("rendered file exceeds size limit")
	}
	return w.buffer.Write(data)
}

type fileError struct {
	name, problem string
	cause         error
}

func (e *fileError) Error() string {
	if e.name == "" {
		return "generated files: " + e.problem
	}
	return fmt.Sprintf("generated file %s: %s", e.name, e.problem)
}
func (e *fileError) Unwrap() error { return e.cause }
