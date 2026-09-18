package typesafe

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestDescribeRuntime(t *testing.T) {
	got := describeRuntime()
	want := regexp.MustCompile(`^go/\S+ \(\w+; \w+\)$`)
	if !want.MatchString(got) {
		t.Errorf("describeRuntime() = %q, want it to match %s", got, want)
	}
	if !strings.Contains(got, runtime.GOOS) || !strings.Contains(got, runtime.GOARCH) {
		t.Errorf("describeRuntime() = %q, want it to name this platform", got)
	}
	if strings.Contains(got, "gogo") {
		t.Errorf("describeRuntime() = %q, want the toolchain's go prefix trimmed once", got)
	}
}

func TestReadEnv(t *testing.T) {
	tests := []struct {
		value string
		want  string
		ok    bool
	}{
		{"value", "value", true},
		{"  padded  ", "padded", true},
		{"", "", false},
		{"   ", "", false},
	}
	for _, test := range tests {
		t.Setenv(EnvBaseURL, test.value)
		got, ok := readEnv(EnvBaseURL)
		if got != test.want || ok != test.ok {
			t.Errorf("readEnv(%q) = %q, %v, want %q, %v", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestFromCodeOrEnv(t *testing.T) {
	t.Setenv(EnvBaseURL, "from-env")
	if got := fromCodeOrEnv("from-code", EnvBaseURL, "fallback"); got != "from-code" {
		t.Errorf("explicit value = %q, want it to win", got)
	}
	if got := fromCodeOrEnv("", EnvBaseURL, "fallback"); got != "from-env" {
		t.Errorf("environment value = %q, want it to win over the fallback", got)
	}
	t.Setenv(EnvBaseURL, "  ")
	if got := fromCodeOrEnv("", EnvBaseURL, "fallback"); got != "fallback" {
		t.Errorf("blank environment value = %q, want the fallback", got)
	}
}
