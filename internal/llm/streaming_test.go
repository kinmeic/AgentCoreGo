package llm

import (
	"context"
	"strings"
	"testing"
)

func collectStream(t *testing.T, input string) []StreamChunk {
	t.Helper()
	ch := make(chan StreamChunk, 16)
	go func() {
		defer close(ch)
		parseSSEStream(context.Background(), strings.NewReader(input), ch)
	}()
	var chunks []StreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}
	return chunks
}

func TestParseSSEStreamComplete(t *testing.T) {
	chunks := collectStream(t, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
	if len(chunks) != 1 || chunks[0].Content != "hello" || chunks[0].Err != nil {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
}

func TestParseSSEStreamAllowsDataWithoutSpace(t *testing.T) {
	chunks := collectStream(t, "data:{\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata:[DONE]\n\n")
	if len(chunks) != 1 || chunks[0].Content != "hello" || chunks[0].Err != nil {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
}

func TestParseSSEStreamRejectsUnexpectedEOF(t *testing.T) {
	chunks := collectStream(t, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want content and terminal error: %#v", len(chunks), chunks)
	}
	if chunks[1].Err == nil {
		t.Fatal("expected an unexpected EOF error")
	}
}

func TestParseSSEStreamReportsMalformedJSON(t *testing.T) {
	chunks := collectStream(t, "data: {broken}\n\ndata: [DONE]\n\n")
	if len(chunks) != 1 || chunks[0].Err == nil {
		t.Fatalf("expected a parse error, got %#v", chunks)
	}
}
