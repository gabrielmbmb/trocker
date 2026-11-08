package lifecycle

import (
	"testing"

	"github.com/gabrielmbmb/trocker/internal/config"
)

func TestParseLocalPortRange(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		content string
		want    config.PortRange
		ok      bool
	}{
		{content: "32768\t60999\n", want: config.PortRange{First: 32768, Last: 60999}, ok: true},
		{content: "60999\t32768\n"},
		{content: "32768\n"},
	} {
		got, ok := parseLocalPortRange(test.content)
		if got != test.want || ok != test.ok {
			t.Errorf("parseLocalPortRange(%q) = %v, %v; want %v, %v", test.content, got, ok, test.want, test.ok)
		}
	}
}
