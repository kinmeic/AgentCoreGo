package observability

import (
	"log/slog"
	"os"
	"strings"
)

// Init configures local structured logging with secret-shaped fields redacted.
func Init(format string) {
	options := &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		key := strings.ToLower(attr.Key)
		for _, sensitive := range []string{"token", "secret", "api_key", "apikey", "authorization", "password", "headers"} {
			if strings.Contains(key, sensitive) {
				return slog.String(attr.Key, "***")
			}
		}
		return attr
	}}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, options)
	} else {
		handler = slog.NewTextHandler(os.Stderr, options)
	}
	slog.SetDefault(slog.New(handler))
}
