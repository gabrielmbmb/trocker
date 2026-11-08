package lifecycle

import (
	"errors"
	"slices"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/plan"
)

func TestSelectReassignments(t *testing.T) {
	t.Parallel()
	ephemeral := config.PortRange{First: 49152, Last: 65535}
	api, web, db := plan.PortKey{Service: "api", Port: 8000}, plan.PortKey{Service: "web", Port: 3000}, plan.PortKey{Service: "db", Port: 5432}
	assigned := map[plan.PortKey]uint16{api: 50001, web: 21000, db: 50000}

	for _, test := range []struct {
		name      string
		requested []plan.PortKey
		want      []plan.PortKey
	}{
		{name: "no request selects only ephemeral assignments", want: []plan.PortKey{api, db}},
		{name: "a request selects outside the ephemeral range, once", requested: []plan.PortKey{web, web}, want: []plan.PortKey{web}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			keys, err := selectReassignments(assigned, test.requested, ephemeral)
			if err != nil || !slices.Equal(keys, test.want) {
				t.Fatalf("selectReassignments = %v, %v; want %v", keys, err, test.want)
			}
		})
	}

	t.Run("unassigned publication is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := selectReassignments(assigned, []plan.PortKey{web, {Service: "web", Port: 3001}}, ephemeral)
		var unassigned *unassignedPortError
		if !errors.As(err, &unassigned) || unassigned.key.Port != 3001 {
			t.Fatalf("selectReassignments = %v", err)
		}
	})
}
