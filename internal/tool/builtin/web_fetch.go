package builtin

import (
	"agent-core/internal/security"
	"agent-core/internal/tool"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WebFetchTool fetches web page content.
// HTTP client is lazily initialized on first use with SSRF protections.
type WebFetchTool struct {
	clientOnce sync.Once
	httpClient *http.Client
}

func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{}
}

func (t *WebFetchTool) getClient() *http.Client {
	t.clientOnce.Do(func() {
		// Custom transport with SSRF protection at the connection level
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

		t.httpClient = &http.Client{
			Timeout:   30 * time.Second,
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
		}
	})
	return t.httpClient
}

func (t *WebFetchTool) Name() string { return "web_fetch" }
func (t *WebFetchTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{NeedsApproval: true, SideEffectScope: "network", RiskLevel: "high", Timeout: 35 * time.Second, MaxOutputBytes: 100_000}
}
func (t *WebFetchTool) Description() string {
	return "Fetch the content of a web page by URL. Returns the page text content."
}
func (t *WebFetchTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The URL to fetch content from.",
			},
			"method": map[string]any{
				"type":        "string",
				"description": "HTTP method (GET, POST). Default: GET.",
				"enum":        []string{"GET", "POST"},
			},
		},
		Required: []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	rawURL, ok := args["url"].(string)
	if !ok || rawURL == "" {
		return "", fmt.Errorf("url is required")
	}

	// Validate URL
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("only http and https URLs are supported")
	}

	// SSRF safety check (initial validation; transport-level check prevents DNS rebinding)
	if err := security.CheckURL(rawURL); err != nil {
		return "", fmt.Errorf("URL blocked: %w", err)
	}

	method := "GET"
	if m, ok := args["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "AgentCore/1.0")
	req.Header.Set("Accept", "text/html,text/plain,application/json")

	resp, err := t.getClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch URL: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024)) // 100KB limit
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	content := string(body)
	if len(content) > 30000 {
		runes := []rune(content)
		if len(runes) > 30000 {
			content = string(runes[:30000]) + "\n... (truncated)"
		}
	}

	return content, nil
}
