package compose_test

import (
	"testing"

	"github.com/gabrielmbmb/trocker/internal/compose"
)

func TestHeldSummary(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		held compose.Held
		want string
	}{
		{held: compose.Held{}, want: "no resources"},
		{held: compose.Held{Volumes: 1}, want: "1 volume"},
		{held: compose.Held{Containers: 1, Networks: 1, Volumes: 1}, want: "1 container, 1 network, 1 volume"},
		{held: compose.Held{Containers: 3, Volumes: 2}, want: "3 containers, 2 volumes"},
	} {
		if got := test.held.Summary(); got != test.want {
			t.Fatalf("Summary(%+v) = %q, want %q", test.held, got, test.want)
		}
	}
}

func TestHeldEmpty(t *testing.T) {
	t.Parallel()

	if !(compose.Held{}).Empty() {
		t.Fatal("zero counts are not empty")
	}
	if (compose.Held{Volumes: 1}).Empty() {
		t.Fatal("retained volume counted as empty")
	}
}
