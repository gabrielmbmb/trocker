package lifecycle

import "testing"

func TestInitDigestCoversApprovedDecisions(t *testing.T) {
	t.Parallel()
	manifest := []byte("version: 1\ncompose:\n  - compose.yaml\n")
	base := initDigest(manifest, false, "target-a", "unix:///docker.sock", false)

	if got := initDigest(manifest, false, "target-a", "unix:///docker.sock", false); got != base {
		t.Fatalf("identical proposals must share a digest: %s != %s", got, base)
	}
	if len(base) != 64 {
		t.Fatalf("digest must be a hex-encoded SHA-256: %q", base)
	}
	for _, test := range []struct {
		name   string
		change func() string
	}{
		{"edited manifest", func() string {
			return initDigest([]byte("version: 1\ncompose:\n  - other.yaml\n"), false, "target-a", "unix:///docker.sock", false)
		}},
		{"created instead of kept", func() string {
			return initDigest(manifest, true, "target-a", "unix:///docker.sock", false)
		}},
		{"different daemon", func() string {
			return initDigest(manifest, false, "target-b", "unix:///docker.sock", false)
		}},
		{"relocated checkout", func() string {
			return initDigest(manifest, false, "target-a", "unix:///docker.sock", true)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.change(); got == base {
				t.Fatalf("%s must require a new approval", test.name)
			}
		})
	}
}

func TestInitDigestSeparatesFields(t *testing.T) {
	t.Parallel()
	// Length-prefixing keeps adjacent fields from colliding into the same input.
	if initDigest([]byte("ab"), false, "c", "d", false) == initDigest([]byte("a"), false, "bc", "d", false) {
		t.Fatal("field boundaries must be part of the digested input")
	}
}
