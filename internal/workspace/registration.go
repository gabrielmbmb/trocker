package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Registration is durable identity metadata, not an inventory of Docker resources.
type Registration struct {
	ID       string   `json:"id"`
	Checkout Checkout `json:"checkout"`
	Targets  []string `json:"targets"`
}

func (r Registration) ProjectName() string { return "trocker-" + strings.ReplaceAll(r.ID, "-", "") }

// Liveness classifies a registration's checkout on disk.
type Liveness int

const (
	// CheckoutLive means the checkout directory exists.
	CheckoutLive Liveness = iota
	// CheckoutGone means the checkout directory and its Git administrative
	// directory are both absent: Git no longer records the worktree either.
	CheckoutGone
	// CheckoutUnconfirmed means the checkout directory is absent while its Git
	// administrative directory survives. An interrupted `git worktree move`
	// looks the same, so this is not proof that the workspace is gone.
	CheckoutUnconfirmed
)

// CheckoutLiveness inspects only the filesystem; it never repairs or writes.
func CheckoutLiveness(checkout Checkout) (Liveness, error) {
	gone, err := pathMissing(checkout.Root)
	if err != nil || !gone {
		return CheckoutLive, err
	}
	gone, err = pathMissing(checkout.GitDir)
	if err != nil || gone {
		return CheckoutGone, err
	}
	return CheckoutUnconfirmed, nil
}

func pathMissing(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}

// Find associates a checkout by its Git administrative directory, not its branch.
// A moved linked worktree is recognized but never persisted by this function.
func Find(ctx context.Context, checkout Checkout, registrations []Registration) (index int, moved bool, err error) {
	marker, err := Identity(checkout)
	if err != nil {
		return -1, false, err
	}
	index = -1
	markerFound := marker == ""
	for i, registration := range registrations {
		previous := registration.Checkout
		if registration.ID == marker {
			markerFound = true
			if previous.GitDir != checkout.GitDir {
				return -1, false, fmt.Errorf("registered Git directory moved or was copied; explicit recovery required")
			}
		}
		if previous.Root == checkout.Root && previous.GitDir != checkout.GitDir {
			return -1, false, fmt.Errorf("workspace path belongs to a different registration; explicit recovery required")
		}
		if previous.GitDir != checkout.GitDir {
			continue
		}
		if index != -1 || previous.CommonDir != checkout.CommonDir {
			return -1, false, fmt.Errorf("ambiguous workspace association; explicit recovery required")
		}
		if marker != "" && marker != registration.ID {
			return -1, false, fmt.Errorf("workspace identity marker conflicts with registry; explicit recovery required")
		}
		index = i
	}
	if !markerFound {
		return -1, false, fmt.Errorf("workspace identity marker has no registry association; explicit recovery required")
	}
	if index == -1 || registrations[index].Checkout.Root == checkout.Root {
		return index, false, nil
	}
	previous := registrations[index].Checkout
	if previous.GitDir == previous.CommonDir {
		return -1, false, fmt.Errorf("moving a main checkout requires explicit recovery")
	}
	if _, err := os.Lstat(previous.Root); !os.IsNotExist(err) {
		return -1, false, fmt.Errorf("previous checkout path still exists or cannot be inspected; explicit recovery required")
	}
	records, err := List(ctx, checkout.Root)
	if err != nil {
		return -1, false, err
	}
	matches := 0
	for _, record := range records {
		if record.Path == checkout.Root && !record.Bare && !record.Prunable {
			matches++
		}
	}
	if matches != 1 {
		return -1, false, fmt.Errorf("git does not confirm a unique worktree move; explicit recovery required")
	}
	return index, true, nil
}
