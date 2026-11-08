package lifecycle

import (
	"errors"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/config"
)

func TestCheckStablePortRange(t *testing.T) {
	t.Parallel()
	linuxDefault := config.PortRange{First: 32768, Last: 60999}
	everyPort := config.PortRange{First: 1024, Last: 65535}
	for _, test := range []struct {
		name      string
		manifest  string
		ephemeral config.PortRange
		rejected  bool
	}{
		{name: "default range ends below the Linux default", manifest: "stable_ports: true\n", ephemeral: linuxDefault},
		{name: "declared range sharing one port is rejected", manifest: "stable_ports: true\nstable_port_range: 30000-32768\n", ephemeral: linuxDefault, rejected: true},
		{name: "default range is rejected where it overlaps", manifest: "stable_ports: true\n", ephemeral: everyPort, rejected: true},
		{name: "dynamic ports skip the check", manifest: "stable_ports: false\n", ephemeral: everyPort},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest, err := config.Parse([]byte("version: 1\ncompose: [compose.yaml]\n" + test.manifest))
			if err != nil {
				t.Fatal(err)
			}
			err = checkStablePortRange(manifest, test.ephemeral)
			var invalid *config.Error
			if rejected := errors.As(err, &invalid) && invalid.Field == "stable_port_range"; rejected != test.rejected || !test.rejected && err != nil {
				t.Fatalf("checkStablePortRange = %v, rejected %v", err, test.rejected)
			}
		})
	}
}
