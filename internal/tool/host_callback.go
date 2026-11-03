package tool

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"agent-core/internal/security"

	"github.com/google/uuid"
)

// HostCallbackProvider calls a host tool with optional per-request HMAC authentication.
type HostCallbackProvider struct {
	name         string
	description  string
	schema       ToolSchema
	callbackURL  string
	secret       string
	allowPrivate bool
	httpClient   *http.Client
}

// HostToolRegistration represents a tool registration request from the host.
type HostToolRegistration struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Schema       ToolSchema `json:"schema"`
	CallbackURL  string     `json:"callback_url"`
	Secret       string     `json:"secret,omitempty"`
	AllowPrivate bool       `json:"allow_private,omitempty"`
}

func NewHostCallback(name, description string, schema ToolSchema, callbackURL string) *HostCallbackProvider {
	return NewHostCallbackWithOptions(HostToolRegistration{Name: name, Description: description, Schema: schema, CallbackURL: callbackURL})
}

func NewHostCallbackWithOptions(registration HostToolRegistration) *HostCallbackProvider {
	provider := &HostCallbackProvider{name: registration.Name, description: registration.Description,
		schema: registration.Schema, callbackURL: registration.CallbackURL,
		secret: registration.Secret, allowPrivate: registration.AllowPrivate}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("DNS lookup failed for %s", host)
		}
		if !provider.allowPrivate {
			for _, ip := range ips {
				if security.IsBlockedIP(ip) {
					return nil, fmt.Errorf("blocked internal callback address")
				}
			}
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	transport.ResponseHeaderTimeout = 30 * time.Second
	provider.httpClient = &http.Client{Timeout: 60 * time.Second, Transport: transport}
	provider.httpClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		origin, _ := url.Parse(provider.callbackURL)
		if request.URL.Scheme != origin.Scheme || request.URL.Host != origin.Host {
			return fmt.Errorf("cross-origin callback redirect blocked")
		}
		return provider.validateURL(request.URL.String())
	}
	return provider
}

func (h *HostCallbackProvider) Name() string        { return h.name }
func (h *HostCallbackProvider) Description() string { return h.description }
func (h *HostCallbackProvider) Schema() ToolSchema  { return h.schema }
func (h *HostCallbackProvider) Capabilities() Capabilities {
	return Capabilities{NeedsApproval: true, SideEffectScope: "external", RiskLevel: "high", Timeout: 65 * time.Second, MaxOutputBytes: 100_000}
}

func (h *HostCallbackProvider) validateURL(value string) error {
	if h.allowPrivate {
		return security.CheckHTTPURL(value)
	}
	return security.CheckURL(value)
}

func (h *HostCallbackProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	if err := h.validateURL(h.callbackURL); err != nil {
		return "", fmt.Errorf("callback URL blocked: %w", err)
	}
	jsonData, err := json.Marshal(map[string]any{"tool": h.name, "args": args})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.callbackURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.secret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		nonce := uuid.NewString()
		mac := hmac.New(sha256.New, []byte(h.secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + nonce + "\n"))
		_, _ = mac.Write(jsonData)
		req.Header.Set("X-AgentCore-Timestamp", timestamp)
		req.Header.Set("X-AgentCore-Nonce", nonce)
		req.Header.Set("X-AgentCore-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("callback request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return "", fmt.Errorf("read callback response: %w", err)
	}
	if len(body) > 1024*1024 {
		return "", fmt.Errorf("callback response exceeds 1 MiB")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("callback returned status %d", resp.StatusCode)
	}
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
	return string(body), nil
}
