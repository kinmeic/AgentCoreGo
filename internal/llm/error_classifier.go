package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ErrorKind categorizes API errors for different handling strategies.
type ErrorKind int

const (
	ErrorUnknown       ErrorKind = iota
	ErrorRateLimit               // 429 - retry with backoff
	ErrorServerError             // 5xx - retry with backoff
	ErrorAuthFailed              // 401 - do not retry
	ErrorForbidden               // 403 - do not retry
	ErrorNotFound                // 404 - do not retry
	ErrorBadRequest              // 400 - do not retry
	ErrorContextLength           // context length exceeded - truncate and retry
	ErrorModelOverloaded         // model overloaded - retry
)

// APIError represents a classified API error.
type APIError struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	Retryable  bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error (%d, %s): %s", e.StatusCode, e.Kind, e.Message)
}

// IsRetryable returns true if the error can be retried.
func (e *APIError) IsRetryable() bool {
	return e.Retryable
}

// KindString returns a human-readable name for the error kind.
func (k ErrorKind) String() string {
	switch k {
	case ErrorRateLimit:
		return "rate_limit"
	case ErrorServerError:
		return "server_error"
	case ErrorAuthFailed:
		return "auth_failed"
	case ErrorForbidden:
		return "forbidden"
	case ErrorNotFound:
		return "not_found"
	case ErrorBadRequest:
		return "bad_request"
	case ErrorContextLength:
		return "context_length"
	case ErrorModelOverloaded:
		return "model_overloaded"
	default:
		return "unknown"
	}
}

// apiErrorResponse represents the error body from OpenAI-compatible APIs.
type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// classifyError examines an HTTP response and returns a classified APIError.
func classifyError(statusCode int, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
	}

	// Try to parse the error body
	var errResp apiErrorResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
	} else {
		apiErr.Message = string(body)
	}

	// Truncate long messages
	if len(apiErr.Message) > 500 {
		apiErr.Message = apiErr.Message[:500] + "..."
	}

	switch statusCode {
	case http.StatusTooManyRequests:
		apiErr.Kind = ErrorRateLimit
		apiErr.Retryable = true

	case http.StatusUnauthorized:
		apiErr.Kind = ErrorAuthFailed
		apiErr.Retryable = false

	case http.StatusForbidden:
		apiErr.Kind = ErrorForbidden
		apiErr.Retryable = false

	case http.StatusNotFound:
		apiErr.Kind = ErrorNotFound
		apiErr.Retryable = false

	case http.StatusBadRequest:
		apiErr.Kind = classifyBadRequest(apiErr.Message)
		apiErr.Retryable = apiErr.Kind == ErrorContextLength

	default:
		if statusCode >= 500 {
			apiErr.Kind = ErrorServerError
			apiErr.Retryable = true
		} else {
			apiErr.Kind = ErrorUnknown
			apiErr.Retryable = false
		}
	}

	return apiErr
}

// classifyBadRequest further classifies 400 errors based on message content.
func classifyBadRequest(message string) ErrorKind {
	msg := strings.ToLower(message)

	if strings.Contains(msg, "context_length") ||
		strings.Contains(msg, "maximum context length") ||
		strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "max_tokens") {
		return ErrorContextLength
	}

	if strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "capacity") ||
		strings.Contains(msg, "try again") {
		return ErrorModelOverloaded
	}

	return ErrorBadRequest
}
