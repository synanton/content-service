package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewLevelParsing(t *testing.T) {
	cases := []struct {
		in    string
		level slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"nope", slog.LevelInfo},
	}
	for _, tc := range cases {
		if got := New(tc.in); !got.Enabled(nil, tc.level) {
			t.Errorf("New(%q) not enabled at %v", tc.in, tc.level)
		}
	}
	if got := New("info"); got.Enabled(nil, slog.LevelDebug) {
		t.Error("info logger must not emit debug")
	}
}

func TestNewEmitsJSON(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.Info("hello", "k", "v")
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if decoded["msg"] != "hello" || decoded["k"] != "v" {
		t.Fatalf("fields = %v", decoded)
	}
}
