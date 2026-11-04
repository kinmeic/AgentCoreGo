package llm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"agent-core/internal/config"
	"golang.org/x/net/http/httpproxy"
)

func configureTransport(cfg config.LLMConfig, transport *http.Transport) error {
	timeout := cfg.RequestTimeoutMs
	if timeout <= 0 {
		timeout = config.DefaultNetworkTimeoutMs
	}
	transport.ResponseHeaderTimeout = time.Duration(timeout) * time.Millisecond
	if cfg.HTTPProxy != "" || cfg.NoProxy != "" {
		proxy := httpproxy.FromEnvironment()
		if cfg.HTTPProxy != "" {
			proxy.HTTPProxy, proxy.HTTPSProxy = cfg.HTTPProxy, cfg.HTTPProxy
		}
		if cfg.NoProxy != "" {
			proxy.NoProxy = cfg.NoProxy
		}
		choose := proxy.ProxyFunc()
		transport.Proxy = func(req *http.Request) (*url.URL, error) { return choose(req.URL) }
	}
	if cfg.CACertFile != "" {
		pem, err := os.ReadFile(cfg.CACertFile)
		if err != nil {
			return fmt.Errorf("read network.caCertFile: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return fmt.Errorf("network.caCertFile contains no CA certificates")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	return nil
}

func (c *Client) retryDelay(attempt int) time.Duration {
	base, factor, capMs := c.cfg.RetryBaseDelayMs, c.cfg.RetryBackoffFactor, c.cfg.RetryMaxDelayMs
	if base < 0 {
		base = 2000
	}
	if factor <= 0 {
		factor = 2
		if base == 0 && capMs == 0 {
			base, capMs = 2000, 60000
		}
	}
	if capMs < 0 {
		capMs = 60000
	}
	ms := math.Min(float64(capMs), float64(base)*math.Pow(factor, float64(attempt-1)))
	if c.cfg.RetryJitter {
		ms *= 0.5 + rand.Float64()*0.5
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// A timeout applies to inactivity between body reads, never total stream age.
// Closing cancels the timer and request, including while a read is blocked.
type idleBody struct {
	io.ReadCloser
	mu       sync.Mutex
	timer    *time.Timer
	timeout  time.Duration
	cancel   context.CancelFunc
	closed   bool
	timedOut atomic.Bool
}

func newIdleBody(body io.ReadCloser, timeout time.Duration, cancel context.CancelFunc) *idleBody {
	b := &idleBody{ReadCloser: body, timeout: timeout, cancel: cancel}
	b.timer = time.AfterFunc(timeout, func() { b.timedOut.Store(true); cancel() })
	return b
}
func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed && n > 0 {
		b.timer.Reset(b.timeout)
	}
	if err != nil && b.timedOut.Load() {
		return n, fmt.Errorf("model response was idle for %s", b.timeout)
	}
	return n, err
}
func (b *idleBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.cancel()
	b.mu.Unlock()
	return b.ReadCloser.Close()
}
