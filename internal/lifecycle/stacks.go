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
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type Stack struct {
	WorkspaceID string
	Worktree    string
	Status      string
	// Missing reports a registration whose checkout is gone.
	Missing bool
	// GitMetadata reports a surviving Git administrative directory, which can
	// also be an interrupted move, so the registration is not swept.
	GitMetadata bool
	Volumes     int
}

// ListStacks reports registrations associated with the selected Docker target.
// It reads the registry, the filesystem, and live containers only: no manifest,
// Git commands, or locks.
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
		liveness, err := workspace.CheckoutLiveness(registration.Checkout)
		if err != nil {
			return nil, err
		}
		containers, err := target.Containers(ctx, registration)
		if err != nil {
			return nil, err
		}
		stack := Stack{
			WorkspaceID: registration.ID,
			Worktree:    registration.Checkout.Root,
			Missing:     liveness != workspace.CheckoutLive,
			GitMetadata: liveness == workspace.CheckoutUnconfirmed,
		}
		var visible bool
		if stack.Missing {
			if _, stack.Volumes, err = target.Retained(ctx, registration); err != nil {
				return nil, err
			}
			stack.Status, visible = missingStatus(containers, stack.Volumes, stack.GitMetadata, all)
		} else {
			stack.Status, visible = summarizeStack(containers, all)
		}
		if !visible {
			continue
		}
		stacks = append(stacks, stack)
	}
	sort.Slice(stacks, func(i, j int) bool {
		if stacks[i].Worktree != stacks[j].Worktree {
			return stacks[i].Worktree < stacks[j].Worktree
		}
		return stacks[i].WorkspaceID < stacks[j].WorkspaceID
	})
	return stacks, nil
}

// missingStatus describes a registration whose checkout is gone. Container
// state still matters: such containers run against a deleted source tree.
// Visibility matches summarizeStack: --all shows every registration, otherwise
// only registrations with running containers.
func missingStatus(containers []api.ContainerSummary, volumes int, gitMetadata, all bool) (status string, visible bool) {
	parts := []string{"missing"}
	states, visible := summarizeStack(containers, all)
	if visible && states != "not created" {
		parts = append(parts, states)
	}
	if gitMetadata {
		parts = append(parts, "git metadata survives")
	}
	switch {
	case volumes == 1:
		parts = append(parts, "1 volume retained")
	case volumes > 1:
		parts = append(parts, fmt.Sprintf("%d volumes retained", volumes))
	}
	return strings.Join(parts, ", "), visible
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
		if _, seen := counts[state]; !seen {
			keys = append(keys, state)
		}
		counts[state]++
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, state := range keys {
		parts[i] = fmt.Sprintf("%s(%d)", state, counts[state])
	}
	return strings.Join(parts, ", ")
}
