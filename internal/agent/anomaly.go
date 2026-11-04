package agent

import (
	"agent-core/internal/model"
	"encoding/json"
)

// Warnings are advisory and bounded per turn, never a replacement hard stop.
func (o *Orchestrator) warnModelAnomalies(cp *Checkpoint, emitter *runEmitter) error {
	if cp.ToolSignatures == nil {
		cp.ToolSignatures = map[string]int{}
	}
	// The most recent assistant turn has just completed all its tool results.
	var calls []model.ToolCall
	for i := len(cp.Messages) - 1; i >= 0; i-- {
		if cp.Messages[i].Role == model.RoleAssistant {
			calls = cp.Messages[i].ToolCalls
			break
		}
	}
	warn := false
	for _, call := range calls {
		var input any
		normalized := call.Function.Arguments
		if json.Unmarshal([]byte(normalized), &input) == nil {
			encoded, _ := json.Marshal(input)
			normalized = string(encoded)
		}
		signature := call.Function.Name + "\x00" + normalized
		cp.ToolSignatures[signature]++
		cp.ToolCallCount++
		if cp.ToolSignatures[signature] >= o.cfg.ModelAnomalyGuard.RepeatedToolCallWarningThreshold {
			warn = true
		}
	}
	guard := o.cfg.ModelAnomalyGuard
	if guard.ToolCallWarningThreshold != nil && cp.ToolCallCount >= *guard.ToolCallWarningThreshold {
		warn = true
	}
	if !warn || cp.BudgetWarnings >= guard.MaxBudgetWarningsPerTurn {
		return nil
	}
	cp.BudgetWarnings++
	message := "Tool use is repeating or exceeding the advisory budget. Check whether the approach is making progress, change strategy if needed, or ask the user for help. This warning does not stop the task."
	cp.Messages = append(cp.Messages, model.Message{Role: model.RoleSystem, Content: message})
	return emitter.emit(model.StreamEvent{Type: "warning", ErrorCode: "model_anomaly", Message: message})
}
