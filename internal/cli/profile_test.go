package cli

import (
	"errors"
	"io"
	"slices"
	"testing"
)

func TestProfileFlagSurface(t *testing.T) {
	t.Parallel()
	root := newRootCommand("test", io.Discard)
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "plan", want: true},
		{name: "doctor", want: true},
		{name: "up", want: true},
		{name: "run", want: true},
		{name: "status", want: true},
		{name: "endpoints", want: true},
		{name: "ports"},
		{name: "init"},
		{name: "start"},
		{name: "stop"},
		{name: "down"},
		{name: "release"},
		{name: "ps"},
		{name: "logs"},
		{name: "exec"},
		{name: "ls"},
	} {
		command, _, err := root.Find([]string{test.name})
		if err != nil {
			t.Fatal(err)
		}
		if got := command.Flags().Lookup("profile") != nil; got != test.want {
			t.Errorf("%s --profile present = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestProfileSelectionTrimsNamesAndRejectsBlanks(t *testing.T) {
	t.Parallel()
	command := newPlanCommand()
	if err := command.Flags().Set("profile", " debug "); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("profile", "workos-local"); err != nil {
		t.Fatal(err)
	}
	profiles, err := profileSelection(command)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(profiles, []string{"debug", "workos-local"}) {
		t.Fatalf("profiles = %v", profiles)
	}

	command = newPlanCommand()
	if err := command.Flags().Set("profile", " "); err != nil {
		t.Fatal(err)
	}
	if _, err := profileSelection(command); !errors.Is(err, ErrUsage) {
		t.Fatalf("blank profile error = %v, want usage error", err)
	}
}
