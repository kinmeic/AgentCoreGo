package compressor

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"context"
	"strings"
	"testing"
)

func testCompressor() *Compressor {
	return NewCompressor(config.CompressionConfig{ThresholdPercent: 0.5, ProtectFirstN: 1, ProtectLastN: 4, TargetRatio: 0.2}, nil)
}

func TestMessageAndToolBudgetIncludesArgumentsAndSchemas(t *testing.T) {
	messages := []model.Message{{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "call", Function: model.ToolFunction{
		Name: "write", Arguments: strings.Repeat("x", 1000),
	}}}}}
	if EstimateMessageTokens(messages) <= EstimateTokens(messages[0].Content) {
		t.Fatal("tool arguments were not included in message budget")
	}
	definitions := []model.ToolDefinition{{Function: model.FuncDef{Name: "large", Description: strings.Repeat("schema ", 1000)}}}
	if EstimateToolDefinitionsTokens(definitions) < 1000 {
		t.Fatal("tool schema budget is unexpectedly small")
	}
}

func TestCompressReportsAccurateMetricsAndFallback(t *testing.T) {
	messages := []model.Message{{Role: model.RoleSystem, Content: "system"}}
	for i := 0; i < 20; i++ {
		messages = append(messages, model.Message{Role: model.RoleUser, Content: strings.Repeat("长会话", 100)})
	}
	result := testCompressor().Compress(context.Background(), messages, 1000)
	if !result.ShouldCompress || result.BeforeTokenEstimate <= result.TokenEstimate {
		t.Fatalf("compression did not reduce budget: %#v", result)
	}
	if result.SummaryUsed {
		t.Fatal("fallback summary was incorrectly reported as LLM generated")
	}
}

func TestDurableFallbackSummaryPreservesGoalsAndTools(t *testing.T) {
	summary, generated := testCompressor().Summarize(context.Background(), []model.Message{
		{Role: model.RoleUser, Content: "必须保留这个目标"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{Function: model.ToolFunction{Name: "write_file"}}}},
		{Role: model.RoleTool, Content: "Error: 失败"},
	})
	if generated || !strings.Contains(summary, "必须保留") || !strings.Contains(summary, "write_file") || !strings.Contains(summary, "失败") {
		t.Fatalf("summary lost key facts: generated=%v summary=%q", generated, summary)
	}
}

func TestCompressHandlesTinyInputs(t *testing.T) {
	for _, messages := range [][]model.Message{nil, {{Role: model.RoleUser, Content: "one"}}, {{Role: model.RoleUser, Content: "one"}, {Role: model.RoleUser, Content: "two"}}} {
		result := testCompressor().Compress(context.Background(), messages, 1)
		if len(result.Messages) == 0 && len(messages) > 0 {
			t.Fatal("tiny input was dropped")
		}
	}
}

func TestChineseInstructionsAreImportant(t *testing.T) {
	if !isImportantUserMessage("不要删除这个文件") || !isImportantToolResult("错误：超时") {
		t.Fatal("Chinese instructions/errors were not protected")
	}
}
