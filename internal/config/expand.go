package config

import "strings"

// Expand substitutes references in a binding already checked by Validate.
func Expand(value string, resolve func(string) (string, error)) (string, error) {
	var result strings.Builder
	for {
		before, after, found := strings.Cut(value, "{{")
		result.WriteString(before)
		if !found {
			return result.String(), nil
		}
		reference, rest, _ := strings.Cut(after, "}}")
		replacement, err := resolve(strings.TrimSpace(reference))
		if err != nil {
			return "", err
		}
		result.WriteString(replacement)
		value = rest
	}
}
