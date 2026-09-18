package typesafe

import (
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// DefaultTimeout is the per-attempt timeout used when [ClientOptions.Timeout]
// is zero.
const DefaultTimeout = 10 * time.Second

// A RetryPolicy decides which failures are retried and how long to wait first.
//
// A policy is a complete setting rather than a patch: a nil *RetryPolicy in
// [ClientOptions] or [RequestOptions] inherits the level above it, and a non-nil
// one replaces it outright. Start from [DefaultRetryPolicy] and change what you
// need, so that a zero field is never mistaken for an unset one:
//
//	policy := typesafe.DefaultRetryPolicy()
//	policy.MaxRetries = 5
//
// Retries have no shared deadline. [ClientOptions.Timeout] bounds each attempt,
// and a whole call is bounded by its [context.Context].
type RetryPolicy struct {
	// MaxRetries is the number of retries after the first attempt. Zero
	// disables retrying.
	MaxRetries int
	// BackoffInitial is the delay before the first retry, doubled before each
	// later one up to BackoffMax.
	BackoffInitial time.Duration
	// BackoffMax caps the doubling.
	BackoffMax time.Duration
	// BackoffJitter is the fraction of each delay, from 0 to 1, that is
	// randomly subtracted so that concurrent clients do not retry in lockstep.
	BackoffJitter float64
	// HTTPStatuses are the response status codes that are retried. A nil slice
	// retries no status; use [StatusRange] to build a contiguous run.
	HTTPStatuses []int
	// RespectRetryAfter honors a Retry-After or retry-after-ms response header
	// in place of the computed backoff.
	RespectRetryAfter bool
	// MaxRetryAfter is the longest server-requested delay that is honored. A
	// longer one falls back to backoff rather than parking the call.
	MaxRetryAfter time.Duration
	// RetryConnection retries a [ConnectionError] that is not a timeout.
	RetryConnection bool
	// RetryTimeout retries an attempt that exceeded its timeout.
	RetryTimeout bool
}

// DefaultRetryPolicy returns the SDK's default policy: two retries of 408, 429,
// and every 5xx, and of connection failures and timeouts, with exponential
// backoff from 500ms to 5s and up to 25% jitter.
//
// It returns a fresh value on each call, so the returned policy can be modified
// freely.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:        2,
		BackoffInitial:    500 * time.Millisecond,
		BackoffMax:        5 * time.Second,
		BackoffJitter:     0.25,
		HTTPStatuses:      append([]int{http.StatusRequestTimeout, http.StatusTooManyRequests}, StatusRange(500, 599)...),
		RespectRetryAfter: true,
		MaxRetryAfter:     time.Minute,
		RetryConnection:   true,
		RetryTimeout:      true,
	}
}

// StatusRange returns the status codes from lo to hi inclusive, for building
// [RetryPolicy.HTTPStatuses]. It returns nil when hi is below lo.
func StatusRange(lo, hi int) []int {
	if hi < lo {
		return nil
	}
	statuses := make([]int, 0, hi-lo+1)
	for status := lo; status <= hi; status++ {
		statuses = append(statuses, status)
	}
	return statuses
}

// RetriesStatus reports whether the policy retries a response status code.
func (p RetryPolicy) RetriesStatus(status int) bool {
	return slices.Contains(p.HTTPStatuses, status)
}

// retriesError reports whether the policy retries a failed attempt. Only a
// [*ConnectionError] is eligible: an unsuccessful response is judged by its
// status, and anything else is this package refusing to send at all.
func (p RetryPolicy) retriesError(err error) bool {
	var connErr *ConnectionError
	if !errors.As(err, &connErr) {
		return false
	}
	if connErr.Timeout > 0 {
		return p.RetryTimeout
	}
	return p.RetryConnection
}

// validate rejects a policy that cannot be applied, so that a bad setting is
// reported when the client or the call is configured rather than on the first
// failure, which may be far away and rare.
func (p RetryPolicy) validate(source string) error {
	if p.MaxRetries < 0 {
		return errorf("%s.MaxRetries must not be negative, got %d", source, p.MaxRetries)
	}
	if p.BackoffInitial < 0 {
		return errorf("%s.BackoffInitial must not be negative, got %s", source, p.BackoffInitial)
	}
	if p.BackoffMax < 0 {
		return errorf("%s.BackoffMax must not be negative, got %s", source, p.BackoffMax)
	}
	if math.IsNaN(p.BackoffJitter) || p.BackoffJitter < 0 || p.BackoffJitter > 1 {
		return errorf("%s.BackoffJitter must be between 0 and 1, got %v", source, p.BackoffJitter)
	}
	if p.MaxRetryAfter < 0 {
		return errorf("%s.MaxRetryAfter must not be negative, got %s", source, p.MaxRetryAfter)
	}
	for _, status := range p.HTTPStatuses {
		if status < 100 || status > 999 {
			return errorf("%s.HTTPStatuses must hold HTTP status codes, got %d", source, status)
		}
	}
	return nil
}

// clone copies the policy so that a caller's later edits to the slice they
// passed cannot change how an existing client retries.
func (p RetryPolicy) clone() RetryPolicy {
	p.HTTPStatuses = slices.Clone(p.HTTPStatuses)
	return p
}

// Delay reports how long to wait before a zero-based retry attempt, given the
// response headers that prompted it, or nil when no response arrived.
//
// A server-requested delay within the policy's ceiling is used exactly, on the
// grounds that the server knows when it will be ready and jitter would only
// make the client early. Everything else uses capped exponential backoff, from
// which jitter is subtracted.
//
// random must return a value in [0, 1); pass [math/rand/v2.Float64] outside of
// a test that needs a fixed delay.
func (p RetryPolicy) Delay(attempt int, header http.Header, random func() float64) time.Duration {
	if p.RespectRetryAfter && header != nil {
		if retryAfter, ok := parseRetryAfter(header, time.Now()); ok && retryAfter <= p.MaxRetryAfter {
			return retryAfter
		}
	}
	// Doubled a step at a time, and stopped once doubling would reach the
	// ceiling, because a shift by a large attempt count overflows the duration
	// and wraps to a negative delay that fires at once. Stopping at half the
	// cap is exact: anything at or above it doubles to the cap or beyond.
	backoff := p.BackoffInitial
	for range attempt {
		if backoff >= p.BackoffMax/2 {
			backoff = p.BackoffMax
			break
		}
		backoff *= 2
	}
	backoff = min(backoff, p.BackoffMax)
	return time.Duration(float64(backoff) * (1 - random()*p.BackoffJitter))
}

// parseRetryAfter reads a server-requested delay from retry-after-ms, or from
// Retry-After as either a number of seconds or an HTTP date.
//
// retry-after-ms wins when both are present: it is the more precise of the two,
// and a service that sends it means it.
func parseRetryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	if raw := header.Get("Retry-After-Ms"); raw != "" {
		if ms, ok := parseDelay(raw); ok {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	raw := header.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	if seconds, ok := parseDelay(raw); ok {
		return time.Duration(seconds * float64(time.Second)), true
	}
	if date, err := http.ParseTime(raw); err == nil {
		return max(0, date.Sub(now)), true
	}
	return 0, false
}

// parseDelay reads a delay a header can be trusted for. A negative, infinite or
// unparseable one is not a delay, and a NaN compares false against every bound
// it would later be checked against, so all of them are refused here rather
// than somewhere they would be easy to miss.
func parseDelay(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	return value, true
}
