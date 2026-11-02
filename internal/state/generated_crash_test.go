package state_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/state"
)

func TestKilledGeneratedWriterCanRecover(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	command := helper(t, directory, "generated-churn", "crash")
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Error(err)
			}
			if err := command.Wait(); err == nil {
				t.Error("terminated writer unexpectedly succeeded")
			}
		}
	})
	var first map[string]string
	if err := json.NewDecoder(output).Decode(&first); err != nil {
		t.Fatal("writer readiness", err)
	}
	consumer := filepath.Dir(filepath.Dir(first["a.bin"]))
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	observed := false
	for !observed {
		entries, err := os.ReadDir(consumer)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".staging-") {
				observed = true
				break
			}
		}
		if !observed {
			select {
			case <-deadline.C:
				t.Fatal("writer never reached staging")
			case <-tick.C:
			}
		}
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed writer succeeded")
	}
	var index struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(readGenerated(t, filepath.Join(consumer, "current.json")), &index); err != nil {
		t.Fatal("interrupted writer damaged pointer", err)
	}
	if !bytes.Equal(readGenerated(t, filepath.Join(consumer, index.Generation, "a.bin")), readGenerated(t, filepath.Join(consumer, index.Generation, "b.bin"))) {
		t.Fatal("pointer selected a partial file set")
	}
	entries, err := os.ReadDir(consumer)
	if err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".staging-") {
			leftovers = append(leftovers, entry.Name())
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := state.Store{Directory: directory}
	recovered, err := store.PublishGenerated(ctx, generatedWorkspace, "", "app", map[string][]byte{"a.bin": []byte("recovered"), "b.bin": []byte("recovered")})
	if err != nil {
		t.Fatal("retry failed or lock leaked", err)
	}
	if string(readGenerated(t, recovered["a.bin"])) != "recovered" {
		t.Fatal("retry selected crash staging")
	}
	for _, name := range leftovers {
		if _, err := os.Stat(filepath.Join(consumer, name)); err != nil {
			t.Fatal("retry deleted uncertain staging", err)
		}
	}
	if len(readGenerated(t, first["a.bin"])) != 1<<20 {
		t.Fatal("original generation removed")
	}
}
