package typesafe_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

// fastRetry is the default policy with the waiting taken out, so that a test
// about how many attempts happen does not also test how long they take.
func fastRetry(maxRetries int) *typesafe.RetryPolicy {
	policy := typesafe.DefaultRetryPolicy()
	policy.MaxRetries = maxRetries
	policy.BackoffInitial = time.Millisecond
	policy.BackoffMax = time.Millisecond
	policy.BackoffJitter = 0
	return &policy
}

func TestRetriesRecoverableStatus(t *testing.T) {
	client, rec := newTestClient(t, func(attempt int, _ recordedRequest) (*http.Response, error) {
		if attempt < 2 {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "down"}, nil), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{"models": []any{}}, nil), nil
	}, &typesafe.ClientOptions{Retry: fastRetry(2)})

	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	requests := rec.Requests()
	if len(requests) != 3 {
		t.Fatalf("%d attempts, want 3", len(requests))
	}
	for attempt, req := range requests {
		got := req.Header.Get("X-Typesafe-Retry-Count")
		want := ""
		if attempt > 0 {
			want = []string{"", "1", "2"}[attempt]
		}
		if got != want {
			t.Errorf("attempt %d sent X-TypeSafe-Retry-Count %q, want %q", attempt, got, want)
		}
	}
}

func TestRetriesAreExhausted(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusTooManyRequests, map[string]any{"error": "slow down"}),
		&typesafe.ClientOptions{Retry: fastRetry(2)})

	_, err := client.ListModels(t.Context(), nil)
	if !errors.Is(err, typesafe.ErrRateLimit) {
		t.Fatalf("error = %v, want ErrRateLimit", err)
	}
	if got := len(rec.Requests()); got != 3 {
		t.Errorf("%d attempts, want 3", got)
	}
}

func TestDoesNotRetryUnrecoverableStatus(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusBadRequest, map[string]any{"error": "bad"}),
		&typesafe.ClientOptions{Retry: fastRetry(5)})

	_, err := client.ListModels(t.Context(), nil)
	if !errors.Is(err, typesafe.ErrBadRequest) {
		t.Fatalf("error = %v, want ErrBadRequest", err)
	}
	if got := len(rec.Requests()); got != 1 {
		t.Errorf("%d attempts, want 1", got)
	}
}

func TestMaxRetriesZeroDisablesRetrying(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusServiceUnavailable, nil),
		&typesafe.ClientOptions{Retry: fastRetry(0)})

	if _, err := client.ListModels(t.Context(), nil); err == nil {
		t.Fatal("ListModels succeeded, want an error")
	}
	if got := len(rec.Requests()); got != 1 {
		t.Errorf("%d attempts, want 1", got)
	}
}

func TestRetriesConnectionFailures(t *testing.T) {
	client, rec := newTestClient(t, func(attempt int, _ recordedRequest) (*http.Response, error) {
		if attempt == 0 {
			return nil, errors.New("connection reset by peer")
		}
		return jsonResponse(http.StatusOK, map[string]any{"models": []any{}}, nil), nil
	}, &typesafe.ClientOptions{Retry: fastRetry(2)})

	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got := len(rec.Requests()); got != 2 {
		t.Errorf("%d attempts, want 2", got)
	}
}

func TestRetryConnectionCanBeDisabled(t *testing.T) {
	policy := fastRetry(5)
	policy.RetryConnection = false
	client, rec := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		return nil, errors.New("connection reset by peer")
	}, &typesafe.ClientOptions{Retry: policy})

	if _, err := client.ListModels(t.Context(), nil); err == nil {
		t.Fatal("ListModels succeeded, want an error")
	}
	if got := len(rec.Requests()); got != 1 {
		t.Errorf("%d attempts, want 1", got)
	}
}

func TestPerCallRetryPolicyReplacesTheClientPolicy(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusServiceUnavailable, nil),
		&typesafe.ClientOptions{Retry: fastRetry(0)})

	if _, err := client.ListModels(t.Context(), &typesafe.RequestOptions{Retry: fastRetry(3)}); err == nil {
		t.Fatal("ListModels succeeded, want an error")
	}
	if got := len(rec.Requests()); got != 4 {
		t.Errorf("%d attempts, want 4", got)
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	policy := typesafe.DefaultRetryPolicy()
	policy.MaxRetries = 1
	policy.BackoffInitial = time.Hour // never used: Retry-After wins
	policy.BackoffMax = time.Hour
	policy.BackoffJitter = 0

	client, _ := newTestClient(t, func(attempt int, _ recordedRequest) (*http.Response, error) {
		if attempt == 0 {
			return jsonResponse(http.StatusTooManyRequests, nil,
				http.Header{"Retry-After-Ms": {"1"}}), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{"models": []any{}}, nil), nil
	}, &typesafe.ClientOptions{Retry: &policy})

	started := time.Now()
	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Errorf("the call took %s; Retry-After was not honored over the backoff", elapsed)
	}
}

func TestCancellationDuringBackoff(t *testing.T) {
	policy := typesafe.DefaultRetryPolicy()
	policy.MaxRetries = 5
	policy.BackoffInitial = time.Minute
	policy.BackoffMax = time.Minute
	policy.BackoffJitter = 0
	policy.RespectRetryAfter = false

	ctx, cancel := context.WithCancel(t.Context())
	client, rec := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		defer cancel()
		return jsonResponse(http.StatusServiceUnavailable, nil, nil), nil
	}, &typesafe.ClientOptions{Retry: &policy})

	_, err := client.ListModels(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if got := len(rec.Requests()); got != 1 {
		t.Errorf("%d attempts, want 1: cancellation should end the wait", got)
	}
}

func TestRetriesTimeouts(t *testing.T) {
	client, rec := newTestClient(t, func(attempt int, req recordedRequest) (*http.Response, error) {
		if attempt == 0 {
			<-req.Ctx.Done()
			return nil, req.Ctx.Err()
		}
		return jsonResponse(http.StatusOK, map[string]any{"models": []any{}}, nil), nil
	}, &typesafe.ClientOptions{Retry: fastRetry(1), Timeout: 20 * time.Millisecond})

	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got := len(rec.Requests()); got != 2 {
		t.Errorf("%d attempts, want 2", got)
	}
}
