package security

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestSchemaValidationDoesNotFetchExternalReferences(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(file, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{server.URL, "file://" + filepath.ToSlash(file)} {
		if _, err := ValidateJSONSchema(map[string]any{"$ref": ref}, map[string]any{}); err == nil {
			t.Errorf("external reference accepted: %s", ref)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("schema validation made %d HTTP requests", requests.Load())
	}
}

func TestSchemaValidationKeepsLocalReferences(t *testing.T) {
	schema := map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object", "required": []string{"value"},
		"definitions": map[string]any{"value": map[string]any{"type": "string"}},
		"properties":  map[string]any{"value": map[string]any{"$ref": "#/definitions/value"}},
	}
	for _, valid := range []bool{false, true} {
		var value any = 1
		if valid {
			value = "ok"
		}
		result, err := ValidateJSONSchema(schema, map[string]any{"value": value})
		if err != nil || result.Valid() != valid {
			t.Fatalf("local validation failed: %#v %v", result, err)
		}
	}
}
