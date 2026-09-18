package typesafe

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"off", LevelOff},
		{"DEBUG", slog.LevelDebug},
		{"  Warn  ", slog.LevelWarn},
	}
	for _, test := range tests {
		got, err := ParseLevel(test.name)
		if err != nil {
			t.Errorf("ParseLevel(%q): %v", test.name, err)
			continue
		}
		if got != test.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", test.name, got, test.want)
		}
	}

	_, err := ParseLevel("loud")
	if err == nil {
		t.Fatal("ParseLevel(\"loud\") succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "debug, info, warn, error, off") {
		t.Errorf("error = %q, want it to list the accepted levels", err)
	}
}

func TestResolveLogger(t *testing.T) {
	t.Run("nil and no environment is silent", func(t *testing.T) {
		t.Setenv(EnvLogLevel, "")
		logger, err := resolveLogger(nil)
		if err != nil {
			t.Fatalf("resolveLogger: %v", err)
		}
		if logger.Enabled(context.Background(), slog.LevelError) {
			t.Error("a client with no logger configured is not silent")
		}
	})

	t.Run("the environment installs one", func(t *testing.T) {
		t.Setenv(EnvLogLevel, "debug")
		logger, err := resolveLogger(nil)
		if err != nil {
			t.Fatalf("resolveLogger: %v", err)
		}
		if !logger.Enabled(context.Background(), slog.LevelDebug) {
			t.Error("TYPESAFE_LOG_LEVEL=debug did not install a debug logger")
		}
	})

	t.Run("off is silent", func(t *testing.T) {
		t.Setenv(EnvLogLevel, "off")
		logger, err := resolveLogger(nil)
		if err != nil {
			t.Fatalf("resolveLogger: %v", err)
		}
		if logger.Enabled(context.Background(), slog.LevelError) {
			t.Error("TYPESAFE_LOG_LEVEL=off still logs")
		}
	})

	t.Run("a bad level names the variable", func(t *testing.T) {
		t.Setenv(EnvLogLevel, "loud")
		_, err := resolveLogger(nil)
		if err == nil {
			t.Fatal("resolveLogger accepted an invalid level")
		}
		if !strings.Contains(err.Error(), EnvLogLevel) {
			t.Errorf("error = %q, want it to name %s", err, EnvLogLevel)
		}
	})

	t.Run("a caller's logger owns its leveling and its sink", func(t *testing.T) {
		t.Setenv(EnvLogLevel, "off")
		var buf bytes.Buffer
		mine := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		logger, err := resolveLogger(mine)
		if err != nil {
			t.Fatalf("resolveLogger: %v", err)
		}
		// The environment says off; the caller's handler says debug, and it wins.
		if !logger.Enabled(context.Background(), slog.LevelDebug) {
			t.Error("the environment overrode the caller's level")
		}
		logger.Debug("hello")
		if !strings.Contains(buf.String(), "hello") {
			t.Errorf("the record did not reach the caller's sink: %q", buf.String())
		}
	})

	// Every record names the SDK, so that a shared log can be filtered down to
	// this package without matching on message text.
	t.Run("records name the SDK", func(t *testing.T) {
		t.Setenv(EnvLogLevel, "")
		var buf bytes.Buffer
		logger, err := resolveLogger(slog.New(slog.NewTextHandler(&buf, nil)))
		if err != nil {
			t.Fatalf("resolveLogger: %v", err)
		}
		logger.Info("sending request")
		if got := buf.String(); !strings.Contains(got, "sdk="+userAgent) {
			t.Errorf("record = %q, want it to carry sdk=%s", got, userAgent)
		}
	})
}

func TestRedact(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"Authorization", "Bearer sk-abcdefghijkl", "Bearer ***ijkl"},
		{"Authorization", "Bearer short", "Bearer ***"},
		{"Proxy-Authorization", "Basic dXNlcjpwYXNzd29yZA==", "Basic ***ZA=="},
		{"X-Api-Key", "abcdefghijklmnop", "***mnop"},
		{"X-Api-Key", "tiny", "***"},
		{"Cookie", "session=abc; theme=dark", "***"},
		{"Set-Cookie", "session=abc", "***"},
		{"X-Trace", "trace-1", "trace-1"},
		{"Content-Type", "application/json", "application/json"},
	}
	for _, test := range tests {
		if got := redact(test.name, test.value); got != test.want {
			t.Errorf("redact(%q, %q) = %q, want %q", test.name, test.value, got, test.want)
		}
	}
}

// TestRedactedHeaderKeepsSecretsOutOfLogs is the property that matters: a
// handler that records the attribute must not be able to see the key.
func TestRedactedHeaderKeepsSecretsOutOfLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	header := make(http.Header)
	header.Set("Authorization", "Bearer sk-supersecretvalue")
	header.Set("Cookie", "session=supersecretsession")
	header.Set("X-Trace", "keep-me")
	logger.Debug("request", "header", redactedHeader(header))

	logged := buf.String()
	for _, secret := range []string{"sk-supersecretvalue", "supersecretsession"} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log leaked %q:\n%s", secret, logged)
		}
	}
	if !strings.Contains(logged, "keep-me") {
		t.Errorf("the log dropped a harmless header:\n%s", logged)
	}
	if !strings.Contains(logged, "alue") {
		t.Errorf("the log dropped the key suffix that tells two keys apart:\n%s", logged)
	}
}

func TestJSONBodyLogValue(t *testing.T) {
	if got := jsonBody(nil).LogValue().Any(); got != nil {
		t.Errorf("an empty body logged as %v, want nil", got)
	}
	if got := jsonBody(`{"a":1}`).LogValue().Any(); got == nil {
		t.Error("a JSON body logged as nil")
	}
	if got := jsonBody("not json").LogValue().String(); got != "not json" {
		t.Errorf("a non-JSON body logged as %q", got)
	}
}
