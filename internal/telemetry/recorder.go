package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Recorder persists model calls, tools and file changes with explicit loss semantics.
type Recorder struct {
	store     *ArtifactStore
	policy    CapturePolicy
	queue     chan any
	writeMu   sync.Mutex
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	dropped   uint64
	closeOnce sync.Once
	closeErr  error
}

func NewRecorder(root string, policy CapturePolicy, queueSize int, maxArtifactBytes int64) (*Recorder, error) {
	if policy != CaptureRedacted && policy != CaptureTraining {
		return nil, fmt.Errorf("telemetry: invalid capture policy %q", policy)
	}
	if queueSize <= 0 {
		queueSize = 256
	}
	store, err := NewArtifactStore(root, maxArtifactBytes)
	if err != nil {
		return nil, err
	}
	recorder := &Recorder{store: store, policy: policy, queue: make(chan any, queueSize)}
	recorder.wg.Add(1)
	go recorder.run()
	return recorder, nil
}

func (r *Recorder) Store() *ArtifactStore { return r.store }
func (r *Recorder) Policy() CapturePolicy { return r.policy }

func (r *Recorder) run() {
	defer r.wg.Done()
	for record := range r.queue {
		_ = r.writeRecord(record)
	}
}

func (r *Recorder) writeRecord(record any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	var runID string
	switch value := record.(type) {
	case ModelCall:
		runID = value.RunID
	case ToolInvocation:
		runID = value.RunID
	case FileChange:
		runID = value.RunID
	default:
		return fmt.Errorf("telemetry: unknown record type %T", record)
	}
	if runID == "" {
		return missingField("record", "", "run_id")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	directory := filepath.Join(r.store.Root(), "records")
	if err := secureMkdirAll(directory); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, runID+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (r *Recorder) enqueue(ctx context.Context, record any) error {
	if r.policy == CaptureTraining {
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return ErrClosed
		}
		return r.writeRecord(record)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	select {
	case r.queue <- record:
		return nil
	default:
		r.dropped++
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecordModelCall captures one physical provider attempt and its wire artifacts.
func (r *Recorder) RecordModelCall(ctx context.Context, meta ModelCallMeta, request, response, stream []byte, result CallResult) error {
	if meta.Purpose == "" {
		meta.Purpose = PurposeMain
	}
	if !meta.Purpose.Valid() {
		return fmt.Errorf("telemetry: invalid purpose %q", meta.Purpose)
	}
	if meta.RunID == "" || meta.RequestID == "" {
		return missingField("model_call", meta.CallID, "run_id/request_id")
	}
	if meta.Attempt <= 0 {
		meta.Attempt = 1
	}
	if meta.StartedAt.IsZero() {
		meta.StartedAt = time.Now()
	}
	if meta.TraceID == "" {
		meta.TraceID = meta.RootID
	}
	if meta.RootID == "" {
		meta.RootID = meta.RunID
	}
	if meta.TraceID == "" {
		meta.TraceID = meta.RootID
	}
	call := ModelCall{SchemaVersion: SchemaVersion, CapturePolicy: r.policy, Fidelity: FidelityFullWire,
		CallID: uuid.NewString(), TraceID: meta.TraceID, RootID: meta.RootID, ParentID: meta.ParentID,
		RunID: meta.RunID, RequestID: meta.RequestID, Purpose: meta.Purpose, Attempt: meta.Attempt,
		Provider: meta.Provider, Model: meta.Model, StartedAt: meta.StartedAt,
		RequestHeaders: RedactHeaders(meta.RequestHeaders), ResponseHeaders: RedactHeaders(meta.ResponseHeaders),
		FinishedAt: result.FinishedAt, HTTPStatus: result.HTTPStatus, Usage: result.Usage,
		FinishReason: result.FinishReason, Error: result.Error,
		CaptureState: CaptureState{CaptureComplete: true}}
	if r.policy == CaptureRedacted {
		call.Fidelity = FidelityRedacted
	}
	if call.FinishedAt.IsZero() {
		call.FinishedAt = time.Now()
	}
	call.LatencyMS = call.FinishedAt.Sub(call.StartedAt).Milliseconds()
	for _, item := range []struct {
		name   string
		data   []byte
		target **ArtifactRef
		kind   string
	}{{"request", request, &call.RawRequest, "application/json"}, {"response", response, &call.RawResponse, "application/json"}, {"stream", stream, &call.RawStream, "text/event-stream"}} {
		if item.data == nil {
			continue
		}
		body, err := sanitizeWireBody(item.data, r.policy)
		if err != nil {
			return err
		}
		ref, err := r.store.Put(item.kind, body)
		if err != nil {
			call.CaptureComplete = false
			call.CaptureError = err.Error()
			if r.policy == CaptureTraining {
				return err
			}
			continue
		}
		if ref.Truncated {
			call.Truncated = true
			call.CaptureComplete = false
			call.Fidelity = FidelityPartial
		}
		*item.target = &ref
	}
	if r.policy == CaptureTraining {
		if err := r.enqueue(ctx, call); err != nil {
			return err
		}
		return nil
	}
	return r.enqueue(ctx, call)
}

// RecordTool captures one structured tool invocation.
func (r *Recorder) RecordTool(ctx context.Context, invocation ToolInvocation) error {
	if invocation.SchemaVersion == 0 {
		invocation.SchemaVersion = SchemaVersion
	}
	invocation.CapturePolicy = r.policy
	if invocation.InvocationID == "" {
		invocation.InvocationID = uuid.NewString()
	}
	if invocation.RunID == "" || invocation.ToolCallID == "" || invocation.Name == "" {
		return missingField("tool", invocation.InvocationID, "run/tool_call/name")
	}
	if !invocation.Status.Valid() {
		return fmt.Errorf("telemetry: invalid tool status %q", invocation.Status)
	}
	if invocation.StartedAt.IsZero() {
		invocation.StartedAt = time.Now()
	}
	if invocation.FinishedAt.IsZero() {
		invocation.FinishedAt = time.Now()
	}
	invocation.DurationMS = invocation.FinishedAt.Sub(invocation.StartedAt).Milliseconds()
	input, err := sanitizeRawMessage(invocation.Input, r.policy)
	if err != nil {
		return err
	}
	invocation.Input = input
	if len(invocation.Output) > 0 {
		output, err := sanitizeRawMessage(invocation.Output, r.policy)
		if err != nil {
			return err
		}
		invocation.Output = output
	}
	if !invocation.CaptureComplete && invocation.CaptureError == "" && !invocation.Dropped && !invocation.Truncated {
		invocation.CaptureComplete = true
	}
	return r.enqueue(ctx, invocation)
}

// RecordFileChange captures first-before/final-after file mutation facts.
func (r *Recorder) RecordFileChange(ctx context.Context, change FileChange) error {
	if change.SchemaVersion == 0 {
		change.SchemaVersion = SchemaVersion
	}
	change.CapturePolicy = r.policy
	if change.ChangeID == "" {
		change.ChangeID = uuid.NewString()
	}
	if change.RunID == "" || change.Path == "" || !change.Operation.Valid() {
		return missingField("file_change", change.ChangeID, "run/path/operation")
	}
	if change.AddedLines == nil || change.DeletedLines == nil {
		return missingField("file_change", change.ChangeID, "added/deleted line counts")
	}
	if change.ChangedAt.IsZero() {
		change.ChangedAt = time.Now()
	}
	if !change.CaptureComplete && change.CaptureError == "" && !change.Dropped && !change.Truncated {
		change.CaptureComplete = true
	}
	return r.enqueue(ctx, change)
}

// Close stops accepting records and flushes the diagnostic queue.
func (r *Recorder) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.queue)
		r.mu.Unlock()
		r.wg.Wait()
	})
	return r.closeErr
}

func (r *Recorder) DroppedCount() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}
