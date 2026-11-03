package tool

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostCallbackSignsAndReadsResponse(t *testing.T) {
	secret := "callback-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(r.Header.Get("X-AgentCore-Timestamp") + "\n" + r.Header.Get("X-AgentCore-Nonce") + "\n"))
		mac.Write(body)
		if !hmac.Equal([]byte(r.Header.Get("X-AgentCore-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"signed ok"}`))
	}))
	defer server.Close()
	provider := NewHostCallbackWithOptions(HostToolRegistration{Name: "host", CallbackURL: server.URL,
		Secret: secret, AllowPrivate: true, Schema: ToolSchema{Type: "object", Properties: map[string]any{}}})
	result, err := provider.Execute(context.Background(), map[string]any{"value": "x"})
	if err != nil || result != "signed ok" {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

func TestHostCallbackRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024+1)))
	}))
	defer server.Close()
	provider := NewHostCallbackWithOptions(HostToolRegistration{Name: "host", CallbackURL: server.URL, AllowPrivate: true})
	if _, err := provider.Execute(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected response limit error, got %v", err)
	}
}
