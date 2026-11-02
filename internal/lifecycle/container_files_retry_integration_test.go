//go:build integration

package lifecycle_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestContainerFilesRerenderAfterBindConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	_, _, contender, _ := upWorkspace(t, ctx)
	for name, data := range map[string]string{"empty.tmpl": "", "compose.yaml": containerFilesCompose, "trocker.yaml": containerFilesManifest, "app.json.tmpl": `{"url":{{json .url}},"id":{{json .id}},"generation":"retry"}`} {
		if err := os.WriteFile(filepath.Join(main, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output := &stealAllocatedPort{ctx: ctx, target: target, registration: registration, contender: contender, limit: 1}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, output)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.occupied) != 1 || !strings.Contains(output.output.String(), "retrying") || result.Endpoints[0].Addresses.Host.Port == output.occupied[0] {
		t.Fatal("bind race was not retried")
	}
	verifyContainerFile(t, ctx, result, "retry")
	containers, err := target.Containers(ctx, contender)
	if err != nil || len(containers) != 1 {
		t.Fatal("unrelated contender was changed", err)
	}
}
