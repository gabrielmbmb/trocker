package lifecycle

import "testing"

func TestWrapperFingerprint(t *testing.T) {
	t.Parallel()
	command := []string{"scripts/env.sh", "--config", "dev"}
	script := []byte("#!/bin/sh\nexec doppler run -- \"$@\"\n")
	base := wrapperFingerprint(command, script)
	for _, test := range []struct {
		name    string
		command []string
		script  []byte
	}{
		{name: "same-length edit", command: command, script: []byte("#!/bin/sh\nexec dopplex run -- \"$@\"\n")},
		{name: "different argument", command: []string{"scripts/env.sh", "--config", "prd"}, script: script},
		{name: "argument boundary moved", command: []string{"scripts/env.sh", "--config dev"}, script: script},
		{name: "argument moved into the script", command: []string{"scripts/env.sh", "--config"}, script: append([]byte("dev"), script...)},
	} {
		if wrapperFingerprint(test.command, test.script) == base {
			t.Errorf("%s must change the fingerprint and require a new approval", test.name)
		}
	}
}
