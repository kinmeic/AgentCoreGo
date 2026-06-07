package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"agent-core/internal/security"
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
	// Use a protected transport with SSRF validation at connection time
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address: %w", err)
			}

			// Resolve DNS and validate before connecting (prevents DNS rebinding)
			ips, err := net.LookupIP(host)
			if err != nil {
				return nil, fmt.Errorf("DNS lookup failed for %s: %w", host, err)
			}

			// Check all resolved IPs against the blocklist
			for _, ip := range ips {
				if security.IsBlockedIP(ip) {
					return nil, fmt.Errorf("blocked: %s resolves to internal IP %s", host, ip.String())
				}
			}

			if len(ips) == 0 {
				return nil, fmt.Errorf("no IP addresses found for %s", host)
			}

			// Connect to the first valid IP
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}

	return &HostCallbackProvider{
		name:        name,
		description: description,
		schema:      schema,
		callbackURL: callbackURL,
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				// Validate redirect target against SSRF rules
				if err := security.CheckURL(req.URL.String()); err != nil {
					return fmt.Errorf("redirect blocked: %w", err)
				}
				return nil
			},
		},
	}
}

func (h *HostCallbackProvider) Name() string        { return h.name }
func (h *HostCallbackProvider) Description() string  { return h.description }
func (h *HostCallbackProvider) Schema() ToolSchema    { return h.schema }

// Execute calls the host's callback URL with the tool arguments.
func (h *HostCallbackProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	// Re-validate URL at execution time (defense in depth)
	if err := security.CheckURL(h.callbackURL); err != nil {
		return "", fmt.Errorf("callback URL blocked: %w", err)
	}

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
