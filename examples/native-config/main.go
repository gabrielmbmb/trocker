// This native demo reads a generated configuration file and calls the demo API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if err := run(context.Background()); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, "error:", err); writeErr != nil {
			os.Exit(1)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	path := os.Getenv("APP_CONFIG")
	if path == "" {
		return errors.New("APP_CONFIG is missing; launch this example with trocker run worker")
	}
	data, err := os.ReadFile(path) //nolint:gosec // The application explicitly consumes the configuration path supplied through APP_CONFIG.
	if err != nil {
		return errors.New("cannot read APP_CONFIG")
	}
	var configuration struct {
		APIURL      string `json:"apiUrl"`
		WorkspaceID string `json:"workspaceId"`
	}
	if err := json.Unmarshal(data, &configuration); err != nil {
		return errors.New("APP_CONFIG is not valid application configuration")
	}
	if configuration.WorkspaceID == "" || configuration.WorkspaceID != os.Getenv("TROCKER_WORKSPACE_ID") {
		return errors.New("configuration does not match the native workspace identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configuration.APIURL, nil)
	if err != nil {
		return errors.New("configured API URL is invalid")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return errors.New("cannot reach the configured API")
	}
	var result struct {
		Service     string `json:"service"`
		WorkspaceID string `json:"workspace_id"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned HTTP %d", response.StatusCode)
	}
	if decodeErr != nil || closeErr != nil {
		return errors.New("cannot read the API response")
	}
	if result.WorkspaceID != configuration.WorkspaceID {
		return errors.New("API response belongs to another workspace")
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
