package typesafe_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

// The cases here are the ways an HTTP client goes subtly wrong under load and
// failure: a header displaced on a retry, a body that stalls after its headers
// arrived, a connection dropped mid-answer, a cancellation mistaken for a
// timeout. Each is cheap to get wrong and expensive to diagnose in production.

// TestSDKHeadersSurviveEveryAttempt is the wide version of the precedence rule.
// A caller may set any header in any capitalization, at either level, and the
// ones this package needs to authenticate and identify the request must still
// arrive intact — on the retry as much as on the first attempt.
func TestSDKHeadersSurviveEveryAttempt(t *testing.T) {
	fast := typesafe.DefaultRetryPolicy()
	fast.MaxRetries = 1
	fast.BackoffInitial = 0
	fast.BackoffMax = 0
	fast.BackoffJitter = 0

	client, rec := newTestClient(t, func(attempt int, _ recordedRequest) (*http.Response, error) {
		if attempt == 0 {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{}, nil), nil
		}
		return jsonResponse(http.StatusOK, systemOneResponse, nil), nil
	}, &typesafe.ClientOptions{
		Retry: &fast,
		Header: http.Header{
			"X-Team":                 {"default"},
			"authorization":          {"bad"},
			"content-type":           {"text/plain"},
			"x-typesafe-retry-count": {"99"},
			"x-typesafe-runtime":     {"bad"},
		},
	})

	_, err := client.SystemOne(t.Context(),
		&typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("?")},
		&typesafe.RequestOptions{Header: http.Header{
			"x-team":                 {"call"},
			"AUTHORIZATION":          {"bad-again"},
			"ACCEPT":                 {"text/plain"},
			"USER-AGENT":             {"bad"},
			"X-TYPESAFE-SDK":         {"bad"},
			"X-TYPESAFE-RUNTIME":     {"bad"},
			"CONTENT-TYPE":           {"text/html"},
			"X-TYPESAFE-RETRY-COUNT": {"88"},
		}})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	requests := rec.Requests()
	if len(requests) != 2 {
		t.Fatalf("%d attempts, want 2", len(requests))
	}
	for attempt, req := range requests {
		for name, want := range map[string]string{
			"Authorization": "Bearer test-key",
			"Accept":        "application/json",
			"Content-Type":  "application/json",
			"X-Team":        "call",
		} {
			if got := req.Header.Get(name); got != want {
				t.Errorf("attempt %d: %s = %q, want %q", attempt, name, got, want)
			}
			if got := req.Header.Values(name); len(got) != 1 {
				t.Errorf("attempt %d: %s sent %d times (%q), want once", attempt, name, len(got), got)
			}
		}
		for _, name := range []string{"User-Agent", "X-Typesafe-Sdk", "X-Typesafe-Runtime"} {
			if got := req.Header.Get(name); strings.Contains(got, "bad") {
				t.Errorf("attempt %d: a caller header displaced %s: %q", attempt, name, got)
			}
		}
		// The caller's 99 and 88 are never sent: the count is this package's to
		// state, and it states nothing on the first attempt.
		want := ""
		if attempt > 0 {
			want = "1"
		}
		if got := req.Header.Get("X-Typesafe-Retry-Count"); got != want {
			t.Errorf("attempt %d: X-TypeSafe-Retry-Count = %q, want %q", attempt, got, want)
		}
	}
}

func TestGETCarriesNoContentTypeOrRetryCount(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, map[string]any{"models": []any{}}),
		&typesafe.ClientOptions{Header: http.Header{
			"content-type":           {"bad"},
			"x-typesafe-retry-count": {"99"},
		}})

	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	req := rec.Last(t)
	for _, name := range []string{"Content-Type", "X-Typesafe-Retry-Count"} {
		if got := req.Header.Values(name); len(got) != 0 {
			t.Errorf("a GET carried %s = %q, want none", name, got)
		}
	}
}

// TestTimeoutCoversTheResponseBody is the failure a client that only times out
// the headers gets wrong: the server answers at once and then stops sending.
// The attempt's timeout has to cover the body too, or the call hangs for as
// long as the server cares to hold it.
func TestTimeoutCoversTheResponseBody(t *testing.T) {
	noRetries := typesafe.DefaultRetryPolicy()
	noRetries.MaxRetries = 0

	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		client, rec := newTestClient(t, func(_ int, req recordedRequest) (*http.Response, error) {
			return withBody(status, stallingBody{ctx: req.Ctx}), nil
		}, &typesafe.ClientOptions{Timeout: 30 * time.Millisecond, Retry: &noRetries})

		_, err := client.ListModels(t.Context(), nil)
		var connErr *typesafe.ConnectionError
		if !errors.As(err, &connErr) {
			t.Fatalf("status %d: error = %v (%T), want a *ConnectionError", status, err, err)
		}
		if connErr.Timeout != 30*time.Millisecond {
			t.Errorf("status %d: Timeout = %s, want 30ms", status, connErr.Timeout)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("status %d: a stalled body did not match context.DeadlineExceeded", status)
		}
		if got := len(rec.Requests()); got != 1 {
			t.Errorf("status %d: %d attempts, want 1", status, got)
		}
	}
}

// TestBodyFailureKeepsItsCause covers a connection dropped part-way through the
// body. The response looked fine; the answer never arrived, so it is a
// connection failure and not the status the headers promised.
func TestBodyFailureKeepsItsCause(t *testing.T) {
	cause := errors.New("socket dropped")
	noConnectionRetries := typesafe.DefaultRetryPolicy()
	noConnectionRetries.RetryConnection = false

	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		client, rec := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
			return withBody(status, failingBody{err: cause}), nil
		}, &typesafe.ClientOptions{Retry: &noConnectionRetries})

		_, err := client.ListModels(t.Context(), nil)
		var connErr *typesafe.ConnectionError
		if !errors.As(err, &connErr) {
			t.Fatalf("status %d: error = %v (%T), want a *ConnectionError", status, err, err)
		}
		if !errors.Is(err, cause) {
			t.Errorf("status %d: the cause did not survive: %v", status, err)
		}
		if connErr.Timeout != 0 {
			t.Errorf("status %d: Timeout = %s, want zero for a dropped body", status, connErr.Timeout)
		}
		// RetryConnection is off, so a dropped body is not tried again even
		// though a 503 would have been.
		if got := len(rec.Requests()); got != 1 {
			t.Errorf("status %d: %d attempts, want 1", status, got)
		}
	}
}

// TestEachAttemptGetsItsOwnTimeout checks what the documentation promises:
// Timeout bounds an attempt, not the call, so a retried call may take several
// times as long and each attempt starts its clock afresh.
func TestEachAttemptGetsItsOwnTimeout(t *testing.T) {
	const timeout = 40 * time.Millisecond
	fast := typesafe.DefaultRetryPolicy()
	fast.MaxRetries = 2
	fast.BackoffInitial = 0
	fast.BackoffMax = 0
	fast.BackoffJitter = 0

	var elapsed []time.Duration
	client, rec := newTestClient(t, func(attempt int, req recordedRequest) (*http.Response, error) {
		started := time.Now()
		if attempt < 2 {
			<-req.Ctx.Done()
			elapsed = append(elapsed, time.Since(started))
			return nil, req.Ctx.Err()
		}
		return jsonResponse(http.StatusOK, map[string]any{"models": []any{}}, nil), nil
	}, &typesafe.ClientOptions{Timeout: timeout, Retry: &fast})

	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got := len(rec.Requests()); got != 3 {
		t.Fatalf("%d attempts, want 3", got)
	}
	// Each timed-out attempt waited its own full timeout rather than sharing
	// one budget, which a shared deadline would have cut short.
	for i, took := range elapsed {
		if took < timeout {
			t.Errorf("attempt %d gave up after %s, short of its own %s timeout", i, took, timeout)
		}
	}
}

// TestCallerAbortDuringAHungRequest separates the two ways a call can stop
// early. A caller who gives up is not a transport failure and is never retried.
func TestCallerAbortDuringAHungRequest(t *testing.T) {
	generous := typesafe.DefaultRetryPolicy()
	generous.MaxRetries = 5
	generous.BackoffInitial = 0
	generous.BackoffMax = 0

	ctx, cancel := context.WithCancel(t.Context())
	client, rec := newTestClient(t, func(_ int, req recordedRequest) (*http.Response, error) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
		<-req.Ctx.Done()
		return nil, req.Ctx.Err()
	}, &typesafe.ClientOptions{Timeout: time.Minute, Retry: &generous})

	_, err := client.ListModels(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Error("a caller's cancellation was reported as a timeout")
	}
	var connErr *typesafe.ConnectionError
	if errors.As(err, &connErr) {
		t.Error("a caller's cancellation was reported as a ConnectionError")
	}
	if got := len(rec.Requests()); got != 1 {
		t.Errorf("%d attempts, want 1: a caller's abort is never retried", got)
	}
}

// TestPerCallRetryLeavesTheClientAlone guards the boundary between the two
// levels: a policy passed to one call is that call's, not the client's.
func TestPerCallRetryLeavesTheClientAlone(t *testing.T) {
	client, _ := newTestClient(t, always(http.StatusServiceUnavailable, nil), nil)
	before := client.Retry()

	perCall := typesafe.DefaultRetryPolicy()
	perCall.MaxRetries = 3
	perCall.BackoffInitial = 0
	perCall.BackoffMax = 0
	perCall.HTTPStatuses = []int{http.StatusServiceUnavailable}

	if _, err := client.ListModels(t.Context(), &typesafe.RequestOptions{Retry: &perCall}); err == nil {
		t.Fatal("ListModels succeeded, want an error")
	}

	after := client.Retry()
	if after.MaxRetries != before.MaxRetries {
		t.Errorf("the client's MaxRetries changed from %d to %d", before.MaxRetries, after.MaxRetries)
	}
	if len(after.HTTPStatuses) != len(before.HTTPStatuses) {
		t.Errorf("the client's status list changed from %d entries to %d",
			len(before.HTTPStatuses), len(after.HTTPStatuses))
	}
}

// TestSystemOneDoesNotMutateTheCallersRequest matters because a request is a
// value a caller may reuse, and filling the model into theirs would make the
// second call to a different client send the first client's default.
func TestSystemOneDoesNotMutateTheCallersRequest(t *testing.T) {
	client, _ := newTestClient(t, always(http.StatusOK, systemOneResponse), nil)

	questions := typesafe.Questions{
		"q": &typesafe.NoulQuestion{Instructions: "?"},
	}
	request := &typesafe.SystemOneRequest{
		State:     map[string]any{"a": 1},
		Questions: questions,
		Extra:     map[string]any{"future": true},
	}
	if _, err := client.SystemOne(t.Context(), request, nil); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	if request.Model != "" {
		t.Errorf("Model was written back as %q", request.Model)
	}
	if len(request.Questions) != 1 || request.Questions["q"] != questions["q"] {
		t.Error("the caller's questions were replaced")
	}
	if len(request.Extra) != 1 || request.Extra["future"] != true {
		t.Error("the caller's Extra was modified")
	}
	if got := request.State.(map[string]any)["a"]; got != 1 {
		t.Errorf("the caller's state was modified: %v", got)
	}
}

// TestRetryPolicyValidatesEveryField keeps the validation honest: a field added
// to the policy without a bound is a setting that fails somewhere far away.
func TestRetryPolicyValidatesEveryField(t *testing.T) {
	tests := []struct {
		name  string
		spoil func(*typesafe.RetryPolicy)
		want  string
	}{
		{"MaxRetries", func(p *typesafe.RetryPolicy) { p.MaxRetries = -1 }, "MaxRetries must not be negative"},
		{"BackoffInitial", func(p *typesafe.RetryPolicy) { p.BackoffInitial = -time.Second }, "BackoffInitial must not be negative"},
		{"BackoffMax", func(p *typesafe.RetryPolicy) { p.BackoffMax = -time.Second }, "BackoffMax must not be negative"},
		{"BackoffJitter low", func(p *typesafe.RetryPolicy) { p.BackoffJitter = -0.1 }, "BackoffJitter must be between 0 and 1"},
		{"BackoffJitter high", func(p *typesafe.RetryPolicy) { p.BackoffJitter = 1.1 }, "BackoffJitter must be between 0 and 1"},
		{"MaxRetryAfter", func(p *typesafe.RetryPolicy) { p.MaxRetryAfter = -time.Second }, "MaxRetryAfter must not be negative"},
		{"HTTPStatuses", func(p *typesafe.RetryPolicy) { p.HTTPStatuses = []int{99} }, "HTTPStatuses must hold HTTP status codes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEnv(t)
			policy := typesafe.DefaultRetryPolicy()
			test.spoil(&policy)

			// A bad policy is refused at both levels it can be set.
			_, err := typesafe.NewClient(&typesafe.ClientOptions{APIKey: "k", Retry: &policy})
			if err == nil {
				t.Fatal("NewClient accepted the policy")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("NewClient error = %q, want it to mention %q", err, test.want)
			}
			if !strings.Contains(err.Error(), "Retry.") {
				t.Errorf("NewClient error = %q, want it to name where the policy came from", err)
			}

			client, _ := newTestClient(t, always(http.StatusOK, map[string]any{"models": []any{}}), nil)
			_, err = client.ListModels(t.Context(), &typesafe.RequestOptions{Retry: &policy})
			if err == nil {
				t.Fatal("the per-call policy was accepted")
			}
			if !strings.Contains(err.Error(), "RequestOptions.Retry.") {
				t.Errorf("per-call error = %q, want it to name where the policy came from", err)
			}
		})
	}
}
