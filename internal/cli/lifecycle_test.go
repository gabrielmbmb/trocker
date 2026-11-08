package cli

import (
	"bytes"
	"errors"
	"testing"
)

func TestUpRejectsConflictingBuildFlags(t *testing.T) {
	t.Parallel()
	err := Run([]string{"up", "--build", "--no-build"}, "test", &bytes.Buffer{})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("conflicting build flags must be a usage error, got %v", err)
	}
}
