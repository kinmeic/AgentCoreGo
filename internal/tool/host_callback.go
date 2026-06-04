package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HostCallbackProvider implements Provider for tools registered by the host application.
type HostCallbackProvider struct {
	name        string
	description string
	schema      ToolSchema
	callbackURL string
	httpClient  *http.Client
}

// NewHostCallback creates a new host callback tool provider.
func NewHostCallback(name, description string, schema ToolSchema, callbackURL string) *HostCallbackProvider {
	return &HostCallbackProvider{
		name:        name,
		description: description,
		schema:      schema,
		callbackURL: callbackURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (h *HostCallbackProvider) Name() string        { return h.name }
func (h *HostCallbackProvider) Description() string  { return h.description }
func (h *HostCallbackProvider) Schema() ToolSchema    { return h.schema }

// Execute calls the host's callback URL with the tool arguments.
func (h *HostCallbackProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	reqBody := map[string]any{
		"tool": h.name,
		"args": args,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal callback request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.callbackURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("create callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("callback request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read callback response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("callback returned status %d: %s", resp.StatusCode, string(body))
	}

	// Try to parse as structured response
	var result struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(body, &result) == nil {
		if result.Error != "" {
			return "", fmt.Errorf("callback error: %s", result.Error)
		}
		if result.Result != "" {
			return result.Result, nil
		}
	}

	// Return raw body if not structured
	return string(body), nil
}

// HostToolRegistration represents a tool registration request from the host.
type HostToolRegistration struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Schema      ToolSchema `json:"schema"`
	CallbackURL string    `json:"callback_url"`
}
