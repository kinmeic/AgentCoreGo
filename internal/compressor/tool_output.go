package compressor

import (
	"fmt"
	"strings"
)

const (
	// ToolOutputThreshold is the character count above which tool output gets compressed.
	ToolOutputThreshold = 2000
	// ToolOutputHead is the number of characters to keep from the start.
	ToolOutputHead = 800
	// ToolOutputTail is the number of characters to keep from the end.
	ToolOutputTail = 400
)

// CompressToolOutput intelligently truncates large tool output, preserving head, tail,
// and key lines (errors, warnings, summary lines).
func CompressToolOutput(output string) string {
	if len(output) <= ToolOutputThreshold {
		return output
	}

	lines := strings.Split(output, "\n")
	if len(lines) <= 5 {
		return compressByChars(output)
	}

	// Collect key lines: errors, warnings, summary, totals
	var keyLines []int
	for i, line := range lines {
		lower := strings.ToLower(line)
		if containsAny(lower, keyLinePatterns...) {
			keyLines = append(keyLines, i)
		}
	}

	// If we have key lines, build output: head + key lines + tail
	if len(keyLines) > 0 {
		return compressWithKeyLines(lines, keyLines)
	}

	return compressByChars(output)
}

var keyLinePatterns = []string{
	"error", "fail", "warning", "fatal", "panic",
	"total", "summary", "result", "passed", "failed",
	"created", "deleted", "modified", "changed",
	"exit code", "status:",
}

func containsAny(s string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func compressByChars(output string) string {
	if len(output) <= ToolOutputHead+ToolOutputTail+100 {
		return output
	}
	// Safe UTF-8 boundary slicing
	runes := []rune(output)
	head := string(runes[:ToolOutputHead])
	tail := string(runes[len(runes)-ToolOutputTail:])
	omitted := len(runes) - ToolOutputHead - ToolOutputTail
	return head + "\n\n... (" + fmt.Sprintf("%d", omitted) + " chars omitted) ...\n\n" + tail
}

func compressWithKeyLines(lines []string, keyLines []int) string {
	totalLines := len(lines)

	// Head: first 15 lines or 20% of total, whichever is smaller
	headEnd := totalLines / 5
	if headEnd > 15 {
		headEnd = 15
	}
	if headEnd < 3 {
		headEnd = 3
	}

	// Tail: last 10 lines or 15% of total
	tailStart := totalLines - totalLines/7
	if tailStart < headEnd+5 {
		tailStart = headEnd + 5
	}
	if tailStart > totalLines-3 {
		tailStart = totalLines - 3
	}

	var result strings.Builder

	// Head
	for i := 0; i < headEnd; i++ {
		result.WriteString(lines[i])
		result.WriteByte('\n')
	}

	// Key lines (not already in head or tail)
	var extractedKeys []string
	for _, idx := range keyLines {
		if idx >= headEnd && idx < tailStart {
			extractedKeys = append(extractedKeys, lines[idx])
		}
	}
	if len(extractedKeys) > 0 {
		result.WriteString("\n--- Key lines ---\n")
		for _, kl := range extractedKeys {
			result.WriteString(kl)
			result.WriteByte('\n')
		}
	}

	omitted := tailStart - headEnd - len(extractedKeys)
	if omitted > 0 {
		result.WriteString("\n... (")
		result.WriteString(formatLines(omitted))
		result.WriteString(" omitted) ...\n\n")
	}

	// Tail
	for i := tailStart; i < totalLines; i++ {
		result.WriteString(lines[i])
		result.WriteByte('\n')
	}

	return result.String()
}

func formatBytes(n int) string {
	if n > 1024 {
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	}
	return fmt.Sprintf("%d chars", n)
}

func formatLines(n int) string {
	return fmt.Sprintf("%d lines", n)
}
