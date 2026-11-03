package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

var sensitiveHeaders = map[string]struct{}{
	"authorization":      {},
	"proxyauthorization": {},
	"authentication":     {},
	"apikey":             {},
	"xapikey":            {},
	"cookie":             {},
	"setcookie":          {},
	"xauthtoken":         {},
	"xaccesstoken":       {},
	"xrefreshtoken":      {},
	"xsessiontoken":      {},
	"xamzsecuritytoken":  {},
	"xgoogapikey":        {},
	"xcsrfToken":         {},
	"xxsrftoken":         {},
	"secretkey":          {},
	"signature":          {},
	"xsignature":         {},
	"proxyauthenticate":  {},
}

var sensitiveJSONKeys = map[string]struct{}{
	"authorization":  {},
	"authentication": {},
	"auth":           {},
	"password":       {},
	"passwd":         {},
	"passphrase":     {},
	"secret":         {},
	"token":          {},
	"accesstoken":    {},
	"refreshtoken":   {},
	"idtoken":        {},
	"sessiontoken":   {},
	"apikey":         {},
	"clientsecret":   {},
	"clientkey":      {},
	"privatekey":     {},
	"credential":     {},
	"credentials":    {},
	"cookie":         {},
	"setcookie":      {},
	"sessionkey":     {},
	"encryptionkey":  {},
	"signingkey":     {},
	"webhooksecret":  {},
}

var mediaJSONKeys = map[string]struct{}{
	"image":      {},
	"imageurl":   {},
	"inputimage": {},
	"audio":      {},
	"audiourl":   {},
	"inputaudio": {},
	"video":      {},
	"videourl":   {},
	"inputvideo": {},
	"filedata":   {},
	"attachment": {},
}

func normalizeKey(s string) string {
	return strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(strings.ToLower(s))
}

func sensitiveHeader(name string) bool {
	normalized := normalizeKey(name)
	if _, ok := sensitiveHeaders[normalized]; ok {
		return true
	}
	for _, suffix := range []string{"apikey", "accesstoken", "refreshtoken", "sessiontoken", "secretkey"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func sensitiveJSONField(name string) bool {
	normalized := normalizeKey(name)
	if _, ok := sensitiveJSONKeys[normalized]; ok {
		return true
	}
	for _, suffix := range []string{"password", "passphrase", "secret", "apikey", "accesstoken", "refreshtoken", "idtoken", "privatekey", "sessionkey", "credential", "credentials"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func mediaJSONField(name string) bool {
	_, ok := mediaJSONKeys[normalizeKey(name)]
	return ok
}

// RedactHeaders returns a deep copy with sensitive HTTP header values removed.
// It never mutates the caller's header map and is applied in both policies.
func RedactHeaders(headers http.Header) http.Header {
	redacted, _ := redactHeaders(headers, 0)
	return redacted
}

func redactHeaders(headers http.Header, maxValueBytes int) (http.Header, bool) {
	if headers == nil {
		return make(http.Header), false
	}
	out := make(http.Header, len(headers))
	truncated := false
	for name, values := range headers {
		if sensitiveHeader(name) {
			out[name] = []string{RedactionMarker}
			continue
		}
		copyValues := make([]string, len(values))
		for i, value := range values {
			if maxValueBytes > 0 && len(value) > maxValueBytes {
				value = value[:maxValueBytes] + "[TRUNCATED]"
				truncated = true
			}
			copyValues[i] = value
		}
		out[name] = copyValues
	}
	return out, truncated
}

func redactDataURL(value string) string {
	lower := strings.ToLower(value)
	for _, prefix := range []string{"data:image/", "data:audio/", "data:video/", "data:application/pdf"} {
		if strings.HasPrefix(lower, prefix) {
			return "[REDACTED MEDIA]"
		}
	}
	return value
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			switch {
			case sensitiveJSONField(key):
				out[key] = RedactionMarker
			case mediaJSONField(key):
				out[key] = "[REDACTED MEDIA]"
			default:
				out[key] = redactValue(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactValue(child)
		}
		return out
	case string:
		return redactDataURL(typed)
	default:
		return value
	}
}

// RedactJSON recursively removes common secret fields and inline media from a
// JSON value. Numbers retain their literal representation. Invalid JSON is
// rejected rather than being changed into a fabricated request or response.
func RedactJSON(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("telemetry: redact invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("telemetry: decode JSON for redaction: %w", err)
	}
	redacted, err := json.Marshal(redactValue(value))
	if err != nil {
		return nil, fmt.Errorf("telemetry: marshal redacted JSON: %w", err)
	}
	return redacted, nil
}

// sanitizeWireBody redacts structured JSON in diagnostic mode. Non-JSON wire
// bytes are preserved so callers can still capture non-JSON provider errors.
func sanitizeWireBody(data []byte, policy CapturePolicy) ([]byte, error) {
	if policy == CaptureTraining || !json.Valid(data) {
		return bytes.Clone(data), nil
	}
	return RedactJSON(data)
}

func sanitizeRawMessage(raw json.RawMessage, policy CapturePolicy) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, missingField("record", "", "json value")
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("telemetry: invalid JSON value")
	}
	if policy == CaptureTraining {
		return json.RawMessage(bytes.Clone(raw)), nil
	}
	redacted, err := RedactJSON(raw)
	return json.RawMessage(redacted), err
}

func limitInlineJSON(raw json.RawMessage, limit int) (json.RawMessage, bool, error) {
	if limit <= 0 || len(raw) <= limit {
		return raw, false, nil
	}
	wrapped, err := json.Marshal(struct {
		Capture      string `json:"capture"`
		OriginalSize int    `json:"original_size"`
		Preview      string `json:"preview"`
	}{
		Capture:      "truncated",
		OriginalSize: len(raw),
		Preview:      string(raw[:limit]),
	})
	if err != nil {
		return nil, false, fmt.Errorf("telemetry: encode truncated JSON preview: %w", err)
	}
	return wrapped, true, nil
}
