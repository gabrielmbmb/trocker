package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// IdentityFile lives in the per-worktree Git directory, never tracked source.
// It detects missing registry associations but is not authority to adopt resources.
const IdentityFile = "trocker-workspace-id"

func Identity(checkout Checkout) (string, error) {
	path := filepath.Join(checkout.GitDir, IdentityFile)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("workspace identity marker must be a regular file; recovery required")
	}
	content, err := os.ReadFile(path) //nolint:gosec // Read only the fixed marker in Git's discovered administrative directory.
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(string(content), "\n")
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return "", fmt.Errorf("invalid workspace identity marker; explicit recovery required")
	}
	return value, nil
}
