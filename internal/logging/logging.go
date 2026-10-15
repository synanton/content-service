// Package logging is the single place where process-wide log output is
// configured. All binaries emit JSON via log/slog to stdout; the level
// comes from CONTENT_LOG_LEVEL (see internal/config). Libraries and jobs
// log through the default logger so audit and operations records share
// one extractable stream. No caller may log raw payloads, tokens, or
// credential material — see internal/security for the audit field
// allowlist and its redaction tests.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New builds a JSON logger at level. Unknown levels fall back to info
// rather than failing startup: log verbosity must never prevent serving.
func New(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

// SetupDefault installs the process-wide JSON logger. Call once, right
// after configuration loads and before any other output.
func SetupDefault(level string) {
	slog.SetDefault(New(level))
}
