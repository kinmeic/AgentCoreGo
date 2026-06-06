package compressor

import (
	"agent-core/internal/model"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Summarizer generates structured summaries of conversation segments using an LLM.
type Summarizer struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewSummarizer creates a new LLM-backed summarizer.
// Reuses the same LLM endpoint as the main agent (configured via config).
func NewSummarizer(baseURL, apiKey, model string) *Summarizer {
	return &Summarizer{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		httpClient: &http.Client{
			Timeout: 10 * time.Second, // Short timeout: summarizer runs in request path
		},
	}
}

// SummaryResult holds the structured summary output.
type SummaryResult struct {
	Summary   string
	Generated bool
	Error     error
}

// SummarizeMessages generates a structured summary for a slice of messages.
// The summary follows a fixed format that preserves key context for the agent.
func (s *Summarizer) SummarizeMessages(ctx context.Context, messages []model.Message) SummaryResult {
	if len(messages) == 0 {
		return SummaryResult{Generated: false}
	}

	conversation := formatConversationForSummary(messages)
	prompt := buildSummaryPrompt(conversation)

	result, err := s.callLLM(ctx, prompt)
	if err != nil {
		log.Printf("[compressor] Summary LLM call failed: %v", err)
		return SummaryResult{
			Summary:   fallbackSummary(messages),
			Generated: false,
			Error:     err,
		}
	}

	return SummaryResult{
		Summary:   result,
		Generated: true,
	}
}

// formatConversationForSummary renders messages into a readable text format.
func formatConversationForSummary(messages []model.Message) string {
	var buf bytes.Buffer
	for _, m := range messages {
		switch m.Role {
		case model.RoleUser:
			buf.WriteString("User: ")
		case model.RoleAssistant:
			buf.WriteString("Assistant: ")
		case model.RoleTool:
			buf.WriteString("Tool result: ")
		case model.RoleSystem:
			continue // Skip system messages in summary input
		}
		content := m.Content
		// Safe UTF-8 truncation
		runes := []rune(content)
		if len(runes) > 500 {
			content = string(runes[:500]) + "..."
		}
		buf.WriteString(content)
		buf.WriteByte('\n')
	}
	return buf.String()
}

const summarySystemPrompt = `You are a conversation summarizer. Summarize the following conversation segment concisely.

Output format (use exactly this structure):
## Conversation Summary
- **User goal**: What the user was trying to accomplish
- **Actions taken**: Key operations performed (file edits, commands run, searches)
- **Key context**: Important facts, decisions, or constraints discovered
- **Open items**: Unresolved questions or pending tasks

Keep the summary under 300 words. Focus on facts and context that would help continue the conversation. Do not include greetings or meta-commentary.`

func buildSummaryPrompt(conversation string) string {
	return fmt.Sprintf("%s\n\n---\n\n%s", summarySystemPrompt, conversation)
}

// fallbackSummary generates a simple text summary when LLM is unavailable.
func fallbackSummary(messages []model.Message) string {
	userCount, assistantCount, toolCount := 0, 0, 0
	for _, m := range messages {
		switch m.Role {
		case model.RoleUser:
			userCount++
		case model.RoleAssistant:
			assistantCount++
		case model.RoleTool:
			toolCount++
		}
	}
	return fmt.Sprintf(
		"[Conversation history compressed: %d user messages, %d assistant messages, %d tool results were summarized. Key context has been preserved in the messages below.]",
		userCount, assistantCount, toolCount,
	)
}

// callLLM makes a minimal OpenAI-compatible chat completion call for summarization.
func (s *Summarizer) callLLM(ctx context.Context, prompt string) (string, error) {
	reqBody := map[string]any{
		"model": s.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.3,
		"max_tokens":  500,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := s.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return result.Choices[0].Message.Content, nil
}
