package telemetry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Trace contains all captured facts for one run.
type Trace struct {
	ModelCalls []ModelCall      `json:"model_calls"`
	Tools      []ToolInvocation `json:"tools"`
	Changes    []FileChange     `json:"changes"`
}

// TraceManifest declares export fidelity instead of implying completeness.
type TraceManifest struct {
	SchemaVersion   int           `json:"schema_version"`
	RunID           string        `json:"run_id"`
	Policy          CapturePolicy `json:"policy"`
	Fidelity        Fidelity      `json:"fidelity"`
	CaptureComplete bool          `json:"capture_complete"`
	ModelCallCount  int           `json:"model_call_count"`
	ToolCount       int           `json:"tool_count"`
	FileChangeCount int           `json:"file_change_count"`
	ExportedAt      time.Time     `json:"exported_at"`
}

// ReadTrace parses a run's append-only record stream without loading raw artifacts.
func (r *Recorder) ReadTrace(runID string) (Trace, error) {
	var trace Trace
	if runID == "" {
		return trace, fmt.Errorf("telemetry: run ID is required")
	}
	file, err := os.Open(filepath.Join(r.store.Root(), "records", runID+".jsonl"))
	if os.IsNotExist(err) {
		return trace, nil
	}
	if err != nil {
		return trace, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &fields); err != nil {
			return trace, err
		}
		switch {
		case fields["tool_call_id"] != nil:
			var invocation ToolInvocation
			if err := json.Unmarshal(scanner.Bytes(), &invocation); err != nil {
				return trace, err
			}
			trace.Tools = append(trace.Tools, invocation)
		case fields["operation"] != nil:
			var change FileChange
			if err := json.Unmarshal(scanner.Bytes(), &change); err != nil {
				return trace, err
			}
			trace.Changes = append(trace.Changes, change)
		case fields["request_id"] != nil:
			var call ModelCall
			if err := json.Unmarshal(scanner.Bytes(), &call); err != nil {
				return trace, err
			}
			trace.ModelCalls = append(trace.ModelCalls, call)
		default:
			return trace, fmt.Errorf("telemetry: unknown trace record schema")
		}
	}
	return trace, scanner.Err()
}

// ExportTrace writes NDJSON and a manifest. Training rejects incomplete samples.
func (r *Recorder) ExportTrace(runID, outputDirectory string) (TraceManifest, error) {
	trace, err := r.ReadTrace(runID)
	if err != nil {
		return TraceManifest{}, err
	}
	manifest := TraceManifest{SchemaVersion: SchemaVersion, RunID: runID, Policy: r.policy,
		Fidelity: FidelityFullWire, CaptureComplete: true, ModelCallCount: len(trace.ModelCalls),
		ToolCount: len(trace.Tools), FileChangeCount: len(trace.Changes), ExportedAt: time.Now().UTC()}
	if r.policy == CaptureRedacted {
		manifest.Fidelity = FidelityRedacted
	}
	for _, call := range trace.ModelCalls {
		if !call.CaptureComplete {
			manifest.CaptureComplete = false
			manifest.Fidelity = FidelityPartial
		}
		if r.policy == CaptureTraining && (call.RawRequest == nil || !call.CaptureComplete) {
			return TraceManifest{}, ErrIncompleteCapture
		}
	}
	if r.policy == CaptureTraining && !manifest.CaptureComplete {
		return TraceManifest{}, ErrIncompleteCapture
	}
	if err := secureMkdirAll(outputDirectory); err != nil {
		return TraceManifest{}, err
	}
	file, err := os.OpenFile(filepath.Join(outputDirectory, "trace.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return TraceManifest{}, err
	}
	writer := bufio.NewWriter(file)
	for _, record := range trace.ModelCalls {
		if err := writeJSONLine(writer, record); err != nil {
			file.Close()
			return TraceManifest{}, err
		}
	}
	for _, record := range trace.Tools {
		if err := writeJSONLine(writer, record); err != nil {
			file.Close()
			return TraceManifest{}, err
		}
	}
	for _, record := range trace.Changes {
		if err := writeJSONLine(writer, record); err != nil {
			file.Close()
			return TraceManifest{}, err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return TraceManifest{}, err
	}
	if err := file.Close(); err != nil {
		return TraceManifest{}, err
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(outputDirectory, "manifest.json"), data, 0o600); err != nil {
		return TraceManifest{}, err
	}
	return manifest, nil
}

func writeJSONLine(writer *bufio.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	return writer.WriteByte('\n')
}

// IsIncomplete reports whether an error is caused by explicit capture loss.
func IsIncomplete(err error) bool { return errors.Is(err, ErrIncompleteCapture) }
