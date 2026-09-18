package typesafe

import (
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
)

// LevelOff is above every level [slog] defines, so a handler thresholded at it
// emits nothing. [ParseLevel] returns it for "off".
const LevelOff = slog.Level(math.MaxInt32)

// levelNames are the accepted level names, from most to least verbose. Reported
// in both messages that reject one, so that a caller is told the same set
// wherever the bad name came from.
const levelNames = "debug, info, warn, error, off"

// ParseLevel converts a level name, in any capitalization, to the level the SDK
// logs at. The names are debug, info, warn, error, and off; "warn" maps to
// [slog.LevelWarn] and "off" to [LevelOff].
func ParseLevel(name string) (slog.Level, error) {
	level, ok := parseLevel(name)
	if !ok {
		return 0, errorf("invalid log level %q; expected one of: %s", name, levelNames)
	}
	return level, nil
}

func parseLevel(name string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	case "off":
		return LevelOff, true
	default:
		return 0, false
	}
}

// resolveLogger returns the logger a client should use.
//
// A library that logs without being asked is a nuisance, so the default is
// silence. [EnvLogLevel] is the one exception: it exists so that a deployed
// program can be made to explain itself without being rebuilt, and it applies
// only when the caller supplied no logger of their own. A caller who did owns
// its leveling, and this package will not second-guess it.
// Every record it returns carries the SDK's identity as an attribute rather
// than as a prefix on each message, which is the one place that fact belongs
// and the form a handler can filter on.
func resolveLogger(logger *slog.Logger) (*slog.Logger, error) {
	if logger != nil {
		return named(logger), nil
	}
	name, ok := readEnv(EnvLogLevel)
	if !ok {
		return named(slog.New(slog.DiscardHandler)), nil
	}
	level, ok := parseLevel(name)
	if !ok {
		return nil, errorf("invalid log level %q from %s; expected one of: %s", name, EnvLogLevel, levelNames)
	}
	if level == LevelOff {
		return named(slog.New(slog.DiscardHandler)), nil
	}
	return named(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))), nil
}

func named(logger *slog.Logger) *slog.Logger {
	return logger.With(slog.String("sdk", userAgent))
}

// A redactedHeader logs a header with its credentials masked.
//
// It is a [slog.LogValuer] rather than an eagerly redacted copy so that the
// copy is only made when a handler actually records the message, which for
// debug logging is almost never.
type redactedHeader http.Header

func (h redactedHeader) LogValue() slog.Value {
	attrs := make([]slog.Attr, 0, len(h))
	for name, values := range h {
		masked := make([]string, len(values))
		for i, value := range values {
			masked[i] = redact(name, value)
		}
		attrs = append(attrs, slog.String(name, strings.Join(masked, ", ")))
	}
	return slog.GroupValue(attrs...)
}

// redact masks a header value that carries a credential, and returns any other
// value unchanged. The name must be in [http.Header] canonical form, which is
// what [http.Header.Set] and [http.Header.Add] produce.
func redact(name, value string) string {
	switch name {
	// A key keeps its last four characters, which is enough to tell two of them
	// apart in a log without disclosing either.
	case "Authorization", "Proxy-Authorization", "X-Api-Key":
		return redactKey(value)
	// A cookie has no identifying prefix worth keeping.
	case "Cookie", "Set-Cookie":
		return "***"
	default:
		return value
	}
}

// minSecretForSuffix is the shortest secret whose last four characters are
// still a small enough fraction of it to disclose.
const minSecretForSuffix = 8

// redactKey keeps an authentication scheme and, for a secret long enough that
// four characters do not give it away, its last four.
func redactKey(value string) string {
	scheme, secret, hasScheme := strings.Cut(value, " ")
	if !hasScheme {
		scheme, secret = "", value
	}
	secret = strings.TrimSpace(secret)
	var tail string
	if len(secret) > minSecretForSuffix {
		tail = secret[len(secret)-4:]
	}
	if scheme != "" {
		return scheme + " ***" + tail
	}
	return "***" + tail
}
