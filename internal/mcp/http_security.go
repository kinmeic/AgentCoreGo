package mcp

import (
	"agent-core/internal/security"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

type headerRoundTripper struct {
	base     http.RoundTripper
	endpoint *url.URL
	headers  map[string]string
}

func (t *headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	if sameOrigin(clone.URL, t.endpoint) {
		for key, value := range t.headers {
			clone.Header.Set(key, value)
		}
	}
	return t.base.RoundTrip(clone)
}

func sameOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && left.Scheme == right.Scheme && left.Host == right.Host
}

func secureHTTPClient(endpoint string, headers map[string]string, allowPrivate bool) (*http.Client, error) {
	if err := validateRemoteURL(endpoint, allowPrivate); err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(endpoint)
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("DNS lookup failed for %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IP addresses for %s", host)
		}
		if !allowPrivate {
			for _, ip := range ips {
				if security.IsBlockedIP(ip) {
					return nil, fmt.Errorf("blocked internal address for %s", host)
				}
			}
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	base.ResponseHeaderTimeout = 30 * time.Second
	base.TLSHandshakeTimeout = 10 * time.Second
	client := &http.Client{Transport: &headerRoundTripper{base: base, endpoint: parsed, headers: headers}}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if !sameOrigin(request.URL, parsed) {
			return fmt.Errorf("cross-origin redirect blocked")
		}
		return validateRemoteURL(request.URL.String(), allowPrivate)
	}
	return client, nil
}
