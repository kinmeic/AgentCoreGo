package vector

import (
	"agent-core/internal/llm"
	"agent-core/internal/telemetry"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Embedder is implemented by OpenAI-compatible and test embedding providers.
type Embedder interface {
	EmbedOne(ctx context.Context, text string) ([]float32, error)
}

// EmbeddingClient generates embeddings using an OpenAI-compatible API.
type EmbeddingClient struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	httpClient *http.Client
	recorder   *telemetry.Recorder
}

// SetRecorder enables embedding wire capture.
func (c *EmbeddingClient) SetRecorder(recorder *telemetry.Recorder) { c.recorder = recorder }

// NewEmbeddingClient creates a new embedding client.
func NewEmbeddingClient(baseURL, apiKey, model string, dimensions int) *EmbeddingClient {
	return &EmbeddingClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		model:      model,
		dimensions: dimensions,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// embeddingRequest is the request body for the embeddings API.
type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

// embeddingResponse is the response from the embeddings API.
type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// Embed generates embeddings for a batch of texts.
func (c *EmbeddingClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := embeddingRequest{
		Model: c.model,
		Input: texts,
	}
	if c.dimensions > 0 {
		reqBody.Dimensions = c.dimensions
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	startedAt := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		captureErr := c.capture(ctx, startedAt, jsonData, nil, nil, err)
		if captureErr != nil {
			return nil, captureErr
		}
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := fmt.Errorf("embedding API error (%d)", resp.StatusCode)
		if captureErr := c.capture(ctx, startedAt, jsonData, resp, body, apiErr); captureErr != nil {
			return nil, captureErr
		}
		return nil, apiErr
	}
	if err := c.capture(ctx, startedAt, jsonData, resp, body, nil); err != nil {
		return nil, err
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(body, &embResp); err != nil {
		return nil, fmt.Errorf("parse embedding response: %w", err)
	}

	// Sort by index to maintain order
	results := make([][]float32, len(texts))
	for _, d := range embResp.Data {
		if d.Index < len(results) {
			results[d.Index] = d.Embedding
		}
	}

	return results, nil
}

// EmbedOne generates an embedding for a single text.
func (c *EmbeddingClient) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	results, err := c.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 || results[0] == nil {
		return nil, fmt.Errorf("no embedding returned")
	}
	return results[0], nil
}

func (c *EmbeddingClient) capture(ctx context.Context, startedAt time.Time, request []byte, response *http.Response, body []byte, callErr error) error {
	if c.recorder == nil {
		return nil
	}
	metadata := llm.CallMetadataFromContext(ctx)
	meta := telemetry.ModelCallMeta{RunID: metadata.RunID, RootID: metadata.RootID, ParentID: metadata.ParentID,
		TraceID: metadata.TraceID, RequestID: uuid.NewString(), Purpose: telemetry.PurposeEmbedding,
		Attempt: 1, Provider: "openai-embedding", Model: c.model, StartedAt: startedAt,
		RequestHeaders: http.Header{"Content-Type": []string{"application/json"}, "Authorization": []string{"Bearer " + c.apiKey}}}
	result := telemetry.CallResult{FinishedAt: time.Now(), Error: errorString(callErr)}
	if response != nil {
		meta.ResponseHeaders = response.Header.Clone()
		result.HTTPStatus = response.StatusCode
	}
	if err := c.recorder.RecordModelCall(ctx, meta, request, body, nil, result); err != nil && c.recorder.Policy() == telemetry.CaptureTraining {
		return fmt.Errorf("embedding capture failed: %w", err)
	}
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Dimensions returns the configured embedding dimensions.
func (c *EmbeddingClient) Dimensions() int {
	return c.dimensions
}
