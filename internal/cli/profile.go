package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func addProfileFlag(command *cobra.Command) {
	command.Flags().StringArray("profile", nil, "Activate a Compose profile (repeatable; overrides trocker.yaml)")
}

func profileSelection(command *cobra.Command) ([]string, error) {
	profiles, err := command.Flags().GetStringArray("profile")
	if err != nil {
		return nil, err
	}
	for i, profile := range profiles {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			return nil, fmt.Errorf("%w: --profile requires a name", ErrUsage)
		}
		profiles[i] = profile
	}
	return profiles, nil
}
