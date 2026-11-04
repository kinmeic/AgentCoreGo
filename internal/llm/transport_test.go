package llm

import (
	"agent-core/internal/config"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStreamIdleTimeoutResetsDuringActiveResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 10; i++ {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := config.DefaultConfig().LLM
	cfg.BaseURL = server.URL
	cfg.Model = "test"
	cfg.StreamIdleTimeoutMs = 60
	cfg.MaxRetries = 0
	client := NewClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := client.CompleteStream(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	content := ""
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		content += chunk.Content
	}
	if content != "xxxxxxxxxx" {
		t.Fatalf("active stream was truncated: %q", content)
	}
}
func TestStreamIdleTimeoutCancelsStalledResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	cfg := config.DefaultConfig().LLM
	cfg.BaseURL = server.URL
	cfg.Model = "test"
	cfg.StreamIdleTimeoutMs = 30
	cfg.MaxRetries = 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := NewClient(cfg).CompleteStream(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for chunk := range stream {
		if chunk.Err != nil {
			failed = true
		}
	}
	if !failed {
		t.Fatal("stalled stream completed successfully")
	}
}
func TestRetryUsesZCodeExponentialCap(t *testing.T) {
	client := NewClient(config.DefaultConfig().LLM)
	client.cfg.RetryJitter = false
	for attempt, expected := range map[int]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 3: 8 * time.Second, 9: 60 * time.Second} {
		if delay := client.retryDelay(attempt); delay != expected {
			t.Fatalf("attempt %d: %v", attempt, delay)
		}
	}
}
