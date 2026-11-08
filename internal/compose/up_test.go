package compose

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

func TestWithBuildPolicy(t *testing.T) {
	t.Parallel()
	project := &types.Project{Services: types.Services{
		"api":      {Name: "api", Build: &types.BuildConfig{Context: "."}},
		"always":   {Name: "always", Build: &types.BuildConfig{Context: "."}, PullPolicy: types.PullPolicyBuild},
		"database": {Name: "database", Image: "postgres:17"},
	}}
	for _, test := range []struct {
		rebuild bool
		want    map[string]string
	}{
		{want: map[string]string{"api": types.PullPolicyNever, "always": types.PullPolicyBuild, "database": ""}},
		{rebuild: true, want: map[string]string{"api": types.PullPolicyBuild, "always": types.PullPolicyBuild, "database": ""}},
	} {
		transformed, err := withBuildPolicy(project, test.rebuild)
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range test.want {
			if got := transformed.Services[name].PullPolicy; got != want {
				t.Errorf("rebuild=%v: %s pull policy = %q, want %q", test.rebuild, name, got, want)
			}
		}
	}
	if project.Services["api"].PullPolicy != "" {
		t.Error("the caller's project was modified")
	}
}
