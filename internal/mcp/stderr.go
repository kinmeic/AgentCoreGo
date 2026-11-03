package mcp

import (
	"regexp"
	"strings"
	"sync"
)

var (
	ansiEscapePattern       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	secretAssignmentPattern = regexp.MustCompile(`(?i)(authorization|api[_-]?key|token|secret|password)(\s*[:=]\s*)([^\s]+)`)
)

type stderrBuffer struct {
	mu        sync.Mutex
	value     string
	truncated bool
}

func (b *stderrBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.value += string(data)
	if len(b.value) > 4096 {
		b.value = b.value[len(b.value)-4096:]
		b.truncated = true
	}
	return len(data), nil
}

func (b *stderrBuffer) summary() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return sanitizeMCPError(b.value)
}

func sanitizeMCPError(value string) string {
	value = ansiEscapePattern.ReplaceAllString(value, "")
	value = secretAssignmentPattern.ReplaceAllString(value, "$1$2[redacted]")
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
