package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/state"
)

type Stack struct {
	WorkspaceID string
	Worktree    string
	Status      string
}

// ListStacks reports registrations associated with the selected Docker target.
// It reads the registry and live containers only: no manifest, Git, or locks.
func ListStacks(ctx context.Context, store state.Store, all bool) (stacks []Stack, err error) {
	registry, err := store.Read()
	if err != nil {
		return nil, err
	}
	if len(registry.Workspaces) == 0 {
		return nil, nil
	}
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()

	for _, registration := range registry.Workspaces {
		if !slices.Contains(registration.Targets, target.ID) {
			continue
		}
		containers, err := target.Containers(ctx, registration)
		if err != nil {
			return nil, err
		}
		status, visible := summarizeStack(containers, all)
		if !visible {
			continue
		}
		stacks = append(stacks, Stack{WorkspaceID: registration.ID, Worktree: registration.Checkout.Root, Status: status})
	}
	sort.Slice(stacks, func(i, j int) bool {
		if stacks[i].Worktree != stacks[j].Worktree {
			return stacks[i].Worktree < stacks[j].Worktree
		}
		return stacks[i].WorkspaceID < stacks[j].WorkspaceID
	})
	return stacks, nil
}

func summarizeStack(containers []api.ContainerSummary, all bool) (string, bool) {
	if len(containers) == 0 {
		if all {
			return "not created", true
		}
		return "", false
	}
	states := make([]string, 0, len(containers))
	for _, ctr := range containers {
		if !all && ctr.State != container.StateRunning {
			continue
		}
		states = append(states, string(ctr.State))
	}
	if len(states) == 0 {
		return "", false
	}
	return combinedStatus(states), true
}

func combinedStatus(states []string) string {
	counts := make(map[string]int, len(states))
	keys := make([]string, 0, len(states))
	for _, state := range states {
		if _, ok := counts[state]; !ok {
			keys = append(keys, state)
		}
		counts[state]++
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, state := range keys {
		parts = append(parts, fmt.Sprintf("%s(%d)", state, counts[state]))
	}
	return strings.Join(parts, ", ")
}
