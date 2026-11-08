package lifecycle

import "testing"

func TestInitDigestCoversApprovedDecisions(t *testing.T) {
	t.Parallel()
	manifest := []byte("version: 1\ncompose:\n  - compose.yaml\n")
	approved := InitProposal{TargetID: "target-a", TargetHost: "unix:///docker.sock"}
	base := approved.digest(manifest, false)

	if got := approved.digest(manifest, false); got != base {
		t.Fatalf("identical proposals must share a digest: %s != %s", got, base)
	}
	if len(base) != 64 {
		t.Fatalf("digest must be a hex-encoded SHA-256: %q", base)
	}
	for _, test := range []struct {
		name     string
		manifest []byte
		creating bool
		proposal InitProposal
	}{
		{"edited manifest", []byte("version: 1\ncompose:\n  - other.yaml\n"), false, approved},
		{"created instead of kept", manifest, true, approved},
		{"different engine", manifest, false, InitProposal{TargetID: "target-b", TargetHost: "unix:///docker.sock"}},
		{"different host", manifest, false, InitProposal{TargetID: "target-a", TargetHost: "unix:///other.sock"}},
		{"relocated checkout", manifest, false, InitProposal{TargetID: "target-a", TargetHost: "unix:///docker.sock", Moved: true}},
		{"environment wrapper", manifest, false, InitProposal{TargetID: "target-a", TargetHost: "unix:///docker.sock", WrapperFingerprint: "abc"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.proposal.digest(test.manifest, test.creating); got == base {
				t.Fatalf("%s must require a new approval", test.name)
			}
		})
	}
}

func TestInitDigestSeparatesFields(t *testing.T) {
	t.Parallel()
	// Length-prefixing keeps adjacent fields from colliding into the same input,
	// so a manifest cannot absorb part of the target identity it is approved for.
	split := InitProposal{TargetID: "bc", TargetHost: "d"}
	shifted := InitProposal{TargetID: "c", TargetHost: "d"}
	if split.digest([]byte("a"), false) == shifted.digest([]byte("ab"), false) {
		t.Fatal("field boundaries must be part of the digested input")
	}
}
