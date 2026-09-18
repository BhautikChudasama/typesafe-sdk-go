package typesafe

import (
	"os"
	"strings"
)

// Environment variables read by [NewClient] when the matching [ClientOptions]
// field is unset. An empty or whitespace-only value counts as unset, so an
// exported-but-empty variable does not shadow an SDK default.
const (
	// EnvAPIKey holds the API key. It is the only required setting, and
	// [ClientOptions.APIKey] takes precedence over it.
	EnvAPIKey = "TYPESAFE_API_KEY" //nolint:gosec // the name of a variable, not a key
	// EnvBaseURL holds the API root, defaulting to [DefaultBaseURL].
	EnvBaseURL = "TYPESAFE_BASE_URL"
	// EnvDefaultModel holds the model used by requests that omit one,
	// defaulting to [DefaultModel].
	EnvDefaultModel = "TYPESAFE_DEFAULT_MODEL"
	// EnvLogLevel holds a level name accepted by [ParseLevel]. It installs a
	// stderr logger only when [ClientOptions.Logger] is nil; a caller who
	// supplies a logger owns its leveling.
	EnvLogLevel = "TYPESAFE_LOG_LEVEL"
)

// readEnv reports a trimmed environment value, and false for a variable that is
// unset, empty, or whitespace only.
func readEnv(name string) (string, bool) {
	value := strings.TrimSpace(os.Getenv(name))
	return value, value != ""
}

// fromCodeOrEnv prefers an explicit value, falls back to the environment, and
// finally to fallback.
func fromCodeOrEnv(fromCode, envVar, fallback string) string {
	if fromCode != "" {
		return fromCode
	}
	if value, ok := readEnv(envVar); ok {
		return value
	}
	return fallback
}
