package llm

import (
	"agent-core/internal/telemetry"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CallMetadata correlates one logical run with physical provider attempts.
type CallMetadata struct {
	RunID    string
	RootID   string
	ParentID string
	TraceID  string
	Purpose  telemetry.Purpose
}

type callMetadataKey struct{}

func WithCallMetadata(ctx context.Context, metadata CallMetadata) context.Context {
	return context.WithValue(ctx, callMetadataKey{}, metadata)
}

// CallMetadataFromContext returns inherited lineage for sidecar purpose overrides.
func CallMetadataFromContext(ctx context.Context) CallMetadata {
	metadata, _ := ctx.Value(callMetadataKey{}).(CallMetadata)
	return metadata
}

func (c *Client) capture(ctx context.Context, attempt int, startedAt time.Time, requestBody []byte, response *http.Response, responseBody []byte, stream bool, callErr error) error {
	if c.recorder == nil {
		return nil
	}
	metadata := CallMetadata{Purpose: telemetry.PurposeMain}
	if value, ok := ctx.Value(callMetadataKey{}).(CallMetadata); ok {
		metadata = value
		if metadata.Purpose == "" {
			metadata.Purpose = telemetry.PurposeMain
		}
	}
	meta := telemetry.ModelCallMeta{RunID: metadata.RunID, RootID: metadata.RootID, ParentID: metadata.ParentID,
		TraceID: metadata.TraceID, RequestID: uuid.NewString(), Purpose: metadata.Purpose, Attempt: attempt,
		Provider: c.Name(), Model: c.ModelName(), StartedAt: startedAt}
	result := telemetry.CallResult{FinishedAt: time.Now()}
	if response != nil {
		meta.ResponseHeaders = response.Header.Clone()
		result.HTTPStatus = response.StatusCode
	}
	meta.RequestHeaders = c.requestHeaders()
	if callErr != nil {
		result.Error = callErr.Error()
	}
	var rawStream []byte
	if stream {
		rawStream = responseBody
		responseBody = nil
	}
	if err := c.recorder.RecordModelCall(ctx, meta, requestBody, responseBody, rawStream, result); err != nil && c.recorder.Policy() == telemetry.CaptureTraining {
		return fmt.Errorf("model-io capture failed: %w", err)
	}
	return nil
}

type recordingBody struct {
	reader    io.ReadCloser
	client    *Client
	ctx       context.Context
	attempt   int
	startedAt time.Time
	request   []byte
	response  *http.Response
	buffer    bytes.Buffer
	once      sync.Once
}

func (b *recordingBody) Read(data []byte) (int, error) {
	n, err := b.reader.Read(data)
	if n > 0 {
		_, _ = b.buffer.Write(data[:n])
	}
	return n, err
}

func (b *recordingBody) Close() error {
	var closeErr, captureErr error
	b.once.Do(func() {
		closeErr = b.reader.Close()
		captureErr = b.client.capture(b.ctx, b.attempt, b.startedAt, b.request, b.response, b.buffer.Bytes(), true, closeErr)
	})
	if closeErr != nil {
		return closeErr
	}
	return captureErr
}
