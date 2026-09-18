package typesafe_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

func TestAPIErrorClasses(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{400, typesafe.ErrBadRequest},
		{401, typesafe.ErrAuthentication},
		{403, typesafe.ErrPermissionDenied},
		{404, typesafe.ErrNotFound},
		{422, typesafe.ErrUnprocessableEntity},
		{429, typesafe.ErrRateLimit},
		{500, typesafe.ErrServerError},
		{503, typesafe.ErrServerError},
		{529, typesafe.ErrServerError},
		{599, typesafe.ErrServerError},
	}
	// Every status must match its own class and no other, so that a caller who
	// switches on them cannot fall into two arms.
	classes := []error{
		typesafe.ErrBadRequest, typesafe.ErrAuthentication, typesafe.ErrPermissionDenied,
		typesafe.ErrNotFound, typesafe.ErrUnprocessableEntity, typesafe.ErrRateLimit,
		typesafe.ErrServerError,
	}
	for _, test := range tests {
		err := error(&typesafe.APIError{Status: test.status})
		for _, class := range classes {
			matched := errors.Is(err, class)
			if want := errors.Is(test.want, class); matched != want {
				t.Errorf("errors.Is(%d, %v) = %v, want %v", test.status, class, matched, want)
			}
		}
	}
}

func TestErrorClassMessages(t *testing.T) {
	tests := []struct {
		class error
		want  string
	}{
		{typesafe.ErrBadRequest, "typesafe: bad request"},
		{typesafe.ErrAuthentication, "typesafe: authentication failed"},
		{typesafe.ErrPermissionDenied, "typesafe: permission denied"},
		{typesafe.ErrNotFound, "typesafe: not found"},
		{typesafe.ErrUnprocessableEntity, "typesafe: unprocessable entity"},
		{typesafe.ErrRateLimit, "typesafe: rate limited"},
		{typesafe.ErrServerError, "typesafe: server error"},
	}
	for _, test := range tests {
		if got := test.class.Error(); got != test.want {
			t.Errorf("Error() = %q, want %q", got, test.want)
		}
	}
}

func TestAPIErrorSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("classifying the ticket: %w", &typesafe.APIError{Status: 429})
	if !errors.Is(err, typesafe.ErrRateLimit) {
		t.Error("a wrapped APIError no longer matches its class")
	}
}

func TestAPIErrorUnclassifiedStatus(t *testing.T) {
	err := error(&typesafe.APIError{Status: 418})
	for _, class := range []error{
		typesafe.ErrBadRequest, typesafe.ErrAuthentication, typesafe.ErrPermissionDenied,
		typesafe.ErrNotFound, typesafe.ErrUnprocessableEntity, typesafe.ErrRateLimit,
		typesafe.ErrServerError,
	} {
		if errors.Is(err, class) {
			t.Errorf("418 matched %v, want no class", class)
		}
	}
	var apiErr *typesafe.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 418 {
		t.Errorf("errors.As did not recover the status")
	}
}

func TestAPIErrorMessages(t *testing.T) {
	long := strings.Repeat("x", 500)
	tests := []struct {
		name string
		body any
		want string
	}{
		{"error object", map[string]any{"error": map[string]any{"message": "invalid api key"}}, "invalid api key"},
		{"error string", map[string]any{"error": "plain string"}, "plain string"},
		{"message", map[string]any{"message": "top-level message"}, "top-level message"},
		{"detail string", map[string]any{"detail": "fastapi style"}, "fastapi style"},
		{
			"detail object",
			map[string]any{"detail": map[string]any{"error_type": "api_usage_error", "message": "Unknown model: x"}},
			"Unknown model: x",
		},
		{
			"detail validation list",
			map[string]any{"detail": []any{
				map[string]any{
					"type": "list_type",
					"loc":  []any{"body", "questions", "q", "score", "criteria"},
					"msg":  "Input should be a valid list",
				},
				map[string]any{
					"type": "too_short",
					"loc":  []any{"body", "questions"},
					"msg":  "Dictionary should have at least 1 item",
				},
			}},
			"questions.q.score.criteria: Input should be a valid list; questions: Dictionary should have at least 1 item",
		},
		{"plain text", "<h1>bad gateway</h1>", "<h1>bad gateway</h1>"},
		{"no message to find", map[string]any{"code": float64(7)}, `{"code":7}`},
		{"empty body", nil, "status code (no body)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := &typesafe.APIError{Status: 400, Body: test.body}
			if got, want := err.Error(), "typesafe: 400 "+test.want; got != want {
				t.Errorf("Error() = %q, want %q", got, want)
			}
		})
	}

	t.Run("truncated", func(t *testing.T) {
		err := &typesafe.APIError{Status: 400, Body: map[string]any{"blob": long}}
		message := err.Error()
		if !strings.HasSuffix(message, "…") {
			t.Errorf("a long body was not truncated: %q", message)
		}
		if got, want := len([]rune(message)), len("typesafe: 400 ")+200+1; got != want {
			t.Errorf("message length = %d runes, want %d", got, want)
		}
	})
}

func TestAPIErrorRetryAfter(t *testing.T) {
	err := &typesafe.APIError{Status: 429, Header: http.Header{"Retry-After": {"3"}}}
	delay, ok := err.RetryAfter()
	if !ok || delay != 3*time.Second {
		t.Errorf("RetryAfter() = %s, %v, want 3s, true", delay, ok)
	}

	none := &typesafe.APIError{Status: 429, Header: http.Header{}}
	if _, ok := none.RetryAfter(); ok {
		t.Error("RetryAfter() reported a delay with no header")
	}
}

func TestErrorResponsesFromTheClient(t *testing.T) {
	header := http.Header{"X-Typesafe-Request-Id": {"req_123"}}
	client, _ := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized,
			map[string]any{"error": map[string]any{"message": "invalid api key"}}, header), nil
	}, nil)

	_, err := client.ListModels(t.Context(), nil)
	var apiErr *typesafe.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want an *APIError", err, err)
	}
	if !errors.Is(err, typesafe.ErrAuthentication) {
		t.Error("a 401 did not match ErrAuthentication")
	}
	if apiErr.RequestID != "req_123" {
		t.Errorf("RequestID = %q", apiErr.RequestID)
	}
	if got := apiErr.Error(); got != "typesafe: 401 invalid api key" {
		t.Errorf("Error() = %q", got)
	}
	if !jsonEqual(apiErr.Body, map[string]any{"error": map[string]any{"message": "invalid api key"}}) {
		t.Errorf("Body = %v", apiErr.Body)
	}
	if got := apiErr.Header.Get("X-Typesafe-Request-Id"); got != "req_123" {
		t.Errorf("Header lost the request id: %q", got)
	}
}

func TestErrorBodyDecoding(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        []byte
		contentType string
		wantBody    any
		wantMessage string
	}{
		{
			name: "html stays text", status: 502, body: []byte("<h1>bad gateway</h1>"), contentType: "text/html",
			wantBody: "<h1>bad gateway</h1>", wantMessage: "typesafe: 502 <h1>bad gateway</h1>",
		},
		{
			name: "empty body", status: 429, body: nil, contentType: "",
			wantBody: nil, wantMessage: "typesafe: 429 status code (no body)",
		},
		{
			// A proxy that rewrites a response often forgets the content type,
			// and the body is still JSON worth reading.
			name: "json without a content type", status: 400,
			body: []byte(`{"message":"no content type"}`), contentType: "",
			wantBody:    map[string]any{"message": "no content type"},
			wantMessage: "typesafe: 400 no content type",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
				return rawResponse(test.status, test.body, test.contentType, nil), nil
			}, nil)

			_, err := client.ListModels(t.Context(), nil)
			var apiErr *typesafe.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v (%T), want an *APIError", err, err)
			}
			if !jsonEqual(apiErr.Body, test.wantBody) {
				t.Errorf("Body = %#v, want %#v", apiErr.Body, test.wantBody)
			}
			if got := err.Error(); got != test.wantMessage {
				t.Errorf("Error() = %q, want %q", got, test.wantMessage)
			}
		})
	}
}

// TestEveryErrorNamesThePackage guards a contract that spans every error type
// here: an error arriving from deep in a caller's stack has to say where it
// came from, and each type used to spell the prefix for itself.
func TestEveryErrorNamesThePackage(t *testing.T) {
	clearEnv(t)
	_, noKey := typesafe.NewClient(nil)
	_, noQuestions := typesafe.NewClient(&typesafe.ClientOptions{APIKey: "k"})
	badRequest := (&typesafe.SystemOneRequest{}).Validate()

	errs := []error{
		typesafe.ErrNoAPIKey,
		typesafe.ErrRateLimit,
		typesafe.ErrServerError,
		&typesafe.APIError{Status: 500},
		&typesafe.APIError{Status: 400, Body: map[string]any{"error": "bad"}},
		&typesafe.ConnectionError{Err: errors.New("boom")},
		&typesafe.ConnectionError{Timeout: time.Second, Err: errors.New("boom")},
		noKey,
		badRequest,
	}
	for _, err := range errs {
		if err == nil {
			t.Error("a case produced no error to check")
			continue
		}
		if !strings.HasPrefix(err.Error(), "typesafe: ") {
			t.Errorf("%T reports %q, want it to name the package", err, err)
		}
	}
	if noQuestions != nil {
		t.Errorf("NewClient with an API key failed: %v", noQuestions)
	}
}

func TestConnectionErrorUnwraps(t *testing.T) {
	cause := errors.New("connection reset by peer")
	err := &typesafe.ConnectionError{Err: cause}
	if !errors.Is(err, cause) {
		t.Error("ConnectionError does not unwrap to its cause")
	}
	if got := err.Error(); !strings.Contains(got, "connection error") {
		t.Errorf("Error() = %q", got)
	}
}
