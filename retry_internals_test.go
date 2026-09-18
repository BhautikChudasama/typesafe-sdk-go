package typesafe

import (
	"math"
	"net/http"
	"slices"
	"testing"
	"time"
)

func header(pairs ...string) http.Header {
	if len(pairs)%2 != 0 {
		panic("header wants name/value pairs")
	}
	h := make(http.Header)
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func noJitter() float64   { return 0 }
func fullJitter() float64 { return 1 }

func TestDefaultRetryPolicy(t *testing.T) {
	policy := DefaultRetryPolicy()
	if policy.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2", policy.MaxRetries)
	}
	if policy.BackoffInitial != 500*time.Millisecond {
		t.Errorf("BackoffInitial = %s", policy.BackoffInitial)
	}
	if policy.BackoffMax != 5*time.Second {
		t.Errorf("BackoffMax = %s", policy.BackoffMax)
	}
	if policy.BackoffJitter != 0.25 {
		t.Errorf("BackoffJitter = %v", policy.BackoffJitter)
	}
	if policy.MaxRetryAfter != time.Minute {
		t.Errorf("MaxRetryAfter = %s", policy.MaxRetryAfter)
	}
	if !policy.RespectRetryAfter || !policy.RetryConnection || !policy.RetryTimeout {
		t.Errorf("policy = %+v, want every toggle on", policy)
	}

	want := append([]int{408, 429}, StatusRange(500, 599)...)
	got := slices.Clone(policy.HTTPStatuses)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("HTTPStatuses = %v, want 408, 429 and 500-599", got)
	}

	// Each call must hand out a policy the caller can edit without reaching
	// into the next one.
	DefaultRetryPolicy().HTTPStatuses[0] = 999
	if DefaultRetryPolicy().HTTPStatuses[0] != 408 {
		t.Error("DefaultRetryPolicy shares its status slice between calls")
	}
}

func TestRetriesStatus(t *testing.T) {
	policy := DefaultRetryPolicy()
	for _, status := range []int{408, 429, 500, 502, 503, 504, 529, 599} {
		if !policy.RetriesStatus(status) {
			t.Errorf("the default policy does not retry %d", status)
		}
	}
	for _, status := range []int{200, 400, 401, 403, 404, 409, 422, 600} {
		if policy.RetriesStatus(status) {
			t.Errorf("the default policy retries %d", status)
		}
	}

	narrow := RetryPolicy{HTTPStatuses: []int{409}}
	if !narrow.RetriesStatus(409) || narrow.RetriesStatus(503) {
		t.Error("a narrowed policy does not consult its own status list")
	}
	empty := RetryPolicy{}
	if empty.RetriesStatus(503) {
		t.Error("a policy with no statuses retried one")
	}
}

func TestStatusRange(t *testing.T) {
	if got := StatusRange(500, 502); !slices.Equal(got, []int{500, 501, 502}) {
		t.Errorf("StatusRange(500, 502) = %v", got)
	}
	if got := StatusRange(500, 500); !slices.Equal(got, []int{500}) {
		t.Errorf("StatusRange(500, 500) = %v", got)
	}
	if got := StatusRange(500, 499); got != nil {
		t.Errorf("StatusRange(500, 499) = %v, want nil", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.October, 21, 7, 28, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
		ok     bool
	}{
		{"seconds", header("Retry-After", "3"), 3 * time.Second, true},
		{"zero seconds", header("Retry-After", "0"), 0, true},
		{"fractional seconds", header("Retry-After", "1.5"), 1500 * time.Millisecond, true},
		{
			"milliseconds win",
			header("Retry-After-Ms", "250", "Retry-After", "3"),
			250 * time.Millisecond, true,
		},
		{
			"http date",
			header("Retry-After", "Wed, 21 Oct 2026 07:28:05 GMT"),
			5 * time.Second, true,
		},
		{
			"http date in the past",
			header("Retry-After", "Wed, 21 Oct 2026 07:27:00 GMT"),
			0, true,
		},
		{"absent", header(), 0, false},
		{"garbage", header("Retry-After", "soon"), 0, false},
		{"negative", header("Retry-After", "-5"), 0, false},
		{"garbage milliseconds", header("Retry-After-Ms", "nope"), 0, false},
		{
			// A malformed retry-after-ms still leaves a usable Retry-After.
			"garbage milliseconds with a good fallback",
			header("Retry-After-Ms", "nope", "Retry-After", "2"),
			2 * time.Second, true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseRetryAfter(test.header, now)
			if ok != test.ok || got != test.want {
				t.Errorf("parseRetryAfter() = %s, %v, want %s, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestRetryDelayBackoff(t *testing.T) {
	policy := DefaultRetryPolicy()
	want := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second,
		4 * time.Second, 5 * time.Second, 5 * time.Second,
	}
	for attempt, wantDelay := range want {
		if got := policy.Delay(attempt, nil, noJitter); got != wantDelay {
			t.Errorf("Delay(%d) = %s, want %s", attempt, got, wantDelay)
		}
	}
}

func TestRetryDelayJitter(t *testing.T) {
	policy := DefaultRetryPolicy()
	if got := policy.Delay(0, nil, fullJitter); got != 375*time.Millisecond {
		t.Errorf("Delay with full jitter = %s, want 375ms", got)
	}
	half := func() float64 { return 0.5 }
	if got := policy.Delay(1, nil, half); got != 875*time.Millisecond {
		t.Errorf("Delay with half jitter = %s, want 875ms", got)
	}

	policy.BackoffJitter = 0
	if got := policy.Delay(0, nil, fullJitter); got != 500*time.Millisecond {
		t.Errorf("Delay with no jitter = %s, want 500ms", got)
	}
}

func TestRetryDelayHonorsRetryAfter(t *testing.T) {
	policy := DefaultRetryPolicy()
	// A server-requested delay is used exactly: jitter would only make the
	// client early.
	if got := policy.Delay(0, header("Retry-After", "2"), fullJitter); got != 2*time.Second {
		t.Errorf("Delay = %s, want 2s", got)
	}
	if got := policy.Delay(3, header("Retry-After-Ms", "10"), fullJitter); got != 10*time.Millisecond {
		t.Errorf("Delay = %s, want 10ms", got)
	}

	// Beyond the ceiling, backoff wins: the call should not park for an hour
	// because a server said so.
	if got := policy.Delay(0, header("Retry-After", "61"), noJitter); got != 500*time.Millisecond {
		t.Errorf("Delay past the ceiling = %s, want the backoff", got)
	}
	if got := policy.Delay(0, header("Retry-After", "60"), noJitter); got != time.Minute {
		t.Errorf("Delay at the ceiling = %s, want 60s", got)
	}
	if got := policy.Delay(0, header("Retry-After-Ms", "60001"), fullJitter); got != 375*time.Millisecond {
		t.Errorf("Delay past the ceiling = %s, want the jittered backoff", got)
	}

	policy.RespectRetryAfter = false
	if got := policy.Delay(0, header("Retry-After", "2"), noJitter); got != 500*time.Millisecond {
		t.Errorf("Delay ignoring Retry-After = %s, want the backoff", got)
	}

	policy = DefaultRetryPolicy()
	policy.MaxRetryAfter = time.Second
	if got := policy.Delay(0, header("Retry-After", "1"), noJitter); got != time.Second {
		t.Errorf("Delay = %s, want 1s", got)
	}
	if got := policy.Delay(0, header("Retry-After", "2"), noJitter); got != 500*time.Millisecond {
		t.Errorf("Delay = %s, want the backoff", got)
	}
}

func TestRetryDelayUsesPolicyShape(t *testing.T) {
	policy := DefaultRetryPolicy()
	policy.BackoffInitial = 100 * time.Millisecond
	policy.BackoffMax = 350 * time.Millisecond
	policy.BackoffJitter = 0.5

	want := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond,
		350 * time.Millisecond, 350 * time.Millisecond,
	}
	for attempt, wantDelay := range want {
		if got := policy.Delay(attempt, nil, noJitter); got != wantDelay {
			t.Errorf("Delay(%d) = %s, want %s", attempt, got, wantDelay)
		}
	}
	if got := policy.Delay(0, nil, fullJitter); got != 50*time.Millisecond {
		t.Errorf("Delay with full jitter = %s, want 50ms", got)
	}
}

// TestRetryDelayDoesNotOverflow guards the doubling. A generous ceiling used to
// let the delay double past the width of a Duration and wrap to a negative one,
// which fires at once — turning a long backoff into a hot retry loop.
func TestRetryDelayDoesNotOverflow(t *testing.T) {
	policy := DefaultRetryPolicy()
	policy.BackoffInitial = time.Second
	policy.BackoffMax = math.MaxInt64
	for attempt := range 100 {
		if got := policy.Delay(attempt, nil, noJitter); got < 0 {
			t.Fatalf("Delay(%d) = %s, want a delay that is not negative", attempt, got)
		}
	}
	// A cap of zero is a policy that does not wait, not one that waits forever.
	policy.BackoffMax = 0
	if got := policy.Delay(5, nil, noJitter); got != 0 {
		t.Errorf("Delay with a zero cap = %s, want none", got)
	}
}

func TestRetriesError(t *testing.T) {
	policy := DefaultRetryPolicy()
	timeout := &ConnectionError{Timeout: time.Second}
	dropped := &ConnectionError{}

	if !policy.retriesError(timeout) || !policy.retriesError(dropped) {
		t.Error("the default policy does not retry connection failures")
	}
	if policy.retriesError(&APIError{Status: 500}) {
		t.Error("an APIError is retried by status, not by retriesError")
	}

	policy.RetryTimeout = false
	if policy.retriesError(timeout) {
		t.Error("RetryTimeout=false still retried a timeout")
	}
	if !policy.retriesError(dropped) {
		t.Error("RetryTimeout=false also stopped retrying dropped connections")
	}

	policy = DefaultRetryPolicy()
	policy.RetryConnection = false
	if policy.retriesError(dropped) {
		t.Error("RetryConnection=false still retried a dropped connection")
	}
	if !policy.retriesError(timeout) {
		t.Error("RetryConnection=false also stopped retrying timeouts")
	}
}
