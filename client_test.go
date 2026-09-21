package typesafe_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	typesafe "github.com/BhautikChudasama/typesafe-sdk-go"
)

func TestNewClientDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv(typesafe.EnvAPIKey, "k")

	client, err := typesafe.NewClient(nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := client.BaseURL(); got != typesafe.DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", got, typesafe.DefaultBaseURL)
	}
	if got := client.DefaultModel(); got != typesafe.DefaultModel {
		t.Errorf("DefaultModel = %q, want %q", got, typesafe.DefaultModel)
	}
	if got := client.Timeout(); got != typesafe.DefaultTimeout {
		t.Errorf("Timeout = %s, want %s", got, typesafe.DefaultTimeout)
	}
	if got, want := client.Retry().MaxRetries, typesafe.DefaultRetryPolicy().MaxRetries; got != want {
		t.Errorf("Retry().MaxRetries = %d, want %d", got, want)
	}
}

func TestNewClientReadsEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv(typesafe.EnvAPIKey, "env-key")
	t.Setenv(typesafe.EnvBaseURL, "https://env.test")
	t.Setenv(typesafe.EnvDefaultModel, "env-model")

	client, err := typesafe.NewClient(nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := client.BaseURL(); got != "https://env.test" {
		t.Errorf("BaseURL = %q", got)
	}
	if got := client.DefaultModel(); got != "env-model" {
		t.Errorf("DefaultModel = %q", got)
	}
}

func TestNewClientPrefersOptionsOverEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv(typesafe.EnvAPIKey, "env-key")
	t.Setenv(typesafe.EnvBaseURL, "https://env.test")
	t.Setenv(typesafe.EnvDefaultModel, "env-model")

	client, err := typesafe.NewClient(&typesafe.ClientOptions{
		APIKey:       "code-key",
		BaseURL:      "https://code.test",
		DefaultModel: "code-model",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := client.BaseURL(); got != "https://code.test" {
		t.Errorf("BaseURL = %q", got)
	}
	if got := client.DefaultModel(); got != "code-model" {
		t.Errorf("DefaultModel = %q", got)
	}
}

func TestNewClientIgnoresBlankEnvironmentValues(t *testing.T) {
	clearEnv(t)
	t.Setenv(typesafe.EnvAPIKey, "k")
	t.Setenv(typesafe.EnvBaseURL, "   ")
	t.Setenv(typesafe.EnvDefaultModel, "")

	client, err := typesafe.NewClient(nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := client.BaseURL(); got != typesafe.DefaultBaseURL {
		t.Errorf("BaseURL = %q, want the default", got)
	}
	if got := client.DefaultModel(); got != typesafe.DefaultModel {
		t.Errorf("DefaultModel = %q, want the default", got)
	}
}

func TestNewClientStripsTrailingSlashes(t *testing.T) {
	clearEnv(t)
	t.Setenv(typesafe.EnvAPIKey, "k")

	for _, baseURL := range []string{"https://x.test/", "https://x.test///"} {
		client, err := typesafe.NewClient(&typesafe.ClientOptions{BaseURL: baseURL})
		if err != nil {
			t.Fatalf("NewClient(%q): %v", baseURL, err)
		}
		if got := client.BaseURL(); got != "https://x.test" {
			t.Errorf("NewClient(%q).BaseURL() = %q, want %q", baseURL, got, "https://x.test")
		}
	}
}

func TestNewClientRejectsBadConfiguration(t *testing.T) {
	negative := typesafe.DefaultRetryPolicy()
	negative.MaxRetries = -1
	jitter := typesafe.DefaultRetryPolicy()
	jitter.BackoffJitter = 1.5
	statuses := typesafe.DefaultRetryPolicy()
	statuses.HTTPStatuses = []int{42}

	tests := []struct {
		name    string
		options typesafe.ClientOptions
		want    string
	}{
		{"no api key", typesafe.ClientOptions{}, "no API key"},
		{"relative base URL", typesafe.ClientOptions{APIKey: "k", BaseURL: "/v1"}, "not an absolute URL"},
		{"negative timeout", typesafe.ClientOptions{APIKey: "k", Timeout: -time.Second}, "Timeout must not be negative"},
		{"negative retries", typesafe.ClientOptions{APIKey: "k", Retry: &negative}, "MaxRetries must not be negative"},
		{"jitter out of range", typesafe.ClientOptions{APIKey: "k", Retry: &jitter}, "BackoffJitter must be between 0 and 1"},
		{"bad status", typesafe.ClientOptions{APIKey: "k", Retry: &statuses}, "must hold HTTP status codes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEnv(t)
			_, err := typesafe.NewClient(&test.options)
			if err == nil {
				t.Fatal("NewClient succeeded, want an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("NewClient error = %q, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestNewClientMissingAPIKeyMatchesSentinel(t *testing.T) {
	clearEnv(t)
	_, err := typesafe.NewClient(nil)
	if !errors.Is(err, typesafe.ErrNoAPIKey) {
		t.Fatalf("NewClient error = %v, want ErrNoAPIKey", err)
	}
	if !strings.Contains(err.Error(), typesafe.EnvAPIKey) {
		t.Errorf("NewClient error = %q, want it to name %s", err, typesafe.EnvAPIKey)
	}
}

func TestNewClientCopiesRetryPolicy(t *testing.T) {
	clearEnv(t)
	policy := typesafe.DefaultRetryPolicy()
	policy.HTTPStatuses = []int{429}
	client, err := typesafe.NewClient(&typesafe.ClientOptions{APIKey: "k", Retry: &policy})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	policy.MaxRetries = 99
	policy.HTTPStatuses[0] = 500
	if got := client.Retry().MaxRetries; got == 99 {
		t.Error("editing the caller's policy changed the client's")
	}
	if got := client.Retry().HTTPStatuses[0]; got != 429 {
		t.Errorf("editing the caller's status slice changed the client's: got %d", got)
	}
}

func TestRequestHeaders(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, map[string]any{"models": []any{}}), nil)

	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	req := rec.Last(t)
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}
	if req.URL != "https://api.test/v1/models" {
		t.Errorf("url = %q", req.URL)
	}

	want := map[string]string{
		"Authorization":  "Bearer test-key",
		"Accept":         "application/json",
		"User-Agent":     "typesafe-sdk-go/" + typesafe.Version,
		"X-Typesafe-Sdk": "typesafe-sdk-go/" + typesafe.Version,
		"X-Typesafe-Runtime": "go/" + strings.TrimPrefix(runtime.Version(), "go") +
			" (" + runtime.GOOS + "; " + runtime.GOARCH + ")",
	}
	for name, value := range want {
		if got := req.Header.Get(name); got != value {
			t.Errorf("header %s = %q, want %q", name, got, value)
		}
	}
	if got := req.Header.Get("Content-Type"); got != "" {
		t.Errorf("a GET carried Content-Type %q, want none", got)
	}
}

func TestHeaderPrecedence(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, map[string]any{"models": []any{}}), &typesafe.ClientOptions{
		Header: http.Header{
			"X-Trace":        {"client"},
			"X-Only-Default": {"yes"},
			// Deliberately lowercase: a caller's literal keeps whatever
			// capitalization it was written with, and must not survive
			// alongside the canonical header this package sets.
			"authorization": {"nope"},
		},
	})

	_, err := client.ListModels(t.Context(), &typesafe.RequestOptions{
		Header: http.Header{"X-Trace": {"call"}, "X-Only-Call": {"yes"}},
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	req := rec.Last(t)
	for name, want := range map[string]string{
		"X-Trace":        "call",
		"X-Only-Default": "yes",
		"X-Only-Call":    "yes",
		"Authorization":  "Bearer test-key",
	} {
		if got := req.Header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if got := req.Header.Values("Authorization"); len(got) != 1 {
		t.Errorf("Authorization sent %d times (%q), want once", len(got), got)
	}
}

func TestSystemOnePayload(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse), nil)

	result, err := client.SystemOne(t.Context(), &typesafe.SystemOneRequest{
		State:     map[string]any{"a": 1},
		Questions: noulQuestion("x"),
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	req := rec.Last(t)
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL != "https://api.test/v1/systemone" {
		t.Errorf("url = %q", req.URL)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}

	want := map[string]any{
		"state":     map[string]any{"a": float64(1)},
		"model":     typesafe.DefaultModel,
		"questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": "x"}},
	}
	if got := req.JSON(t); !jsonEqual(got, want) {
		t.Errorf("body = %s, want %s", mustJSON(got), mustJSON(want))
	}

	answer, err := result.Answers.Noul("q")
	if err != nil {
		t.Fatalf("Answers.Noul: %v", err)
	}
	if answer.Noul != 0.5 {
		t.Errorf("Noul = %v, want 0.5", answer.Noul)
	}
	if result.Usage != (typesafe.Usage{InputTokens: 1, OutputTokens: 2}) {
		t.Errorf("Usage = %+v", result.Usage)
	}
	if result.Model != "jev-1.13" {
		t.Errorf("Model = %q", result.Model)
	}
}

func TestSystemOneModelResolution(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse),
		&typesafe.ClientOptions{DefaultModel: "client-default"})

	request := &typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("q")}
	if _, err := client.SystemOne(t.Context(), request, nil); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if got := rec.Last(t).JSON(t).(map[string]any)["model"]; got != "client-default" {
		t.Errorf("model = %v, want the client default", got)
	}
	if request.Model != "" {
		t.Errorf("SystemOne wrote %q into the caller's request", request.Model)
	}

	request.Model = "per-call"
	if _, err := client.SystemOne(t.Context(), request, nil); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if got := rec.Last(t).JSON(t).(map[string]any)["model"]; got != "per-call" {
		t.Errorf("model = %v, want the per-call override", got)
	}
}

func TestSystemOneForwardsExtra(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse), nil)

	_, err := client.SystemOne(t.Context(), &typesafe.SystemOneRequest{
		State:     "s",
		Questions: noulQuestion("q"),
		Extra:     map[string]any{"future_option": nil, "nested": map[string]any{"enabled": true}},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	body := rec.Last(t).JSON(t).(map[string]any)
	if value, ok := body["future_option"]; !ok || value != nil {
		t.Errorf("future_option = %v (present %v), want an explicit null", value, ok)
	}
	if !jsonEqual(body["nested"], map[string]any{"enabled": true}) {
		t.Errorf("nested = %v", body["nested"])
	}
}

func TestSystemOneRejectsCollidingExtra(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse), nil)

	_, err := client.SystemOne(t.Context(), &typesafe.SystemOneRequest{
		State:     "s",
		Questions: noulQuestion("q"),
		Extra:     map[string]any{"state": "shadow"},
	}, nil)
	if err == nil {
		t.Fatal("SystemOne succeeded, want a collision error")
	}
	if !strings.Contains(err.Error(), `Extra key "state" collides`) {
		t.Errorf("error = %q", err)
	}
	if got := len(rec.Requests()); got != 0 {
		t.Errorf("%d requests were sent, want none", got)
	}
}

func TestSystemOneValidatesBeforeSending(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse), nil)

	tests := []struct {
		name      string
		questions typesafe.Questions
		want      string
	}{
		{"empty", typesafe.Questions{}, "at least one question is required"},
		{"nil map", nil, "at least one question is required"},
		{
			"one score criterion",
			typesafe.Questions{"q": &typesafe.ScoreQuestion{Criteria: typesafe.ScoreCriteria{"only"}}},
			`score question "q" has 1 criteria; between 2 and 10 levels are required`,
		},
		{
			"no score criteria",
			typesafe.Questions{"q": &typesafe.ScoreQuestion{}},
			`score question "q" has 0 criteria`,
		},
		{
			// The service caps a rubric at ten levels.
			"too many score criteria",
			typesafe.Questions{"q": &typesafe.ScoreQuestion{Criteria: make(typesafe.ScoreCriteria, 11)}},
			`score question "q" has 11 criteria; between 2 and 10 levels are required`,
		},
		{
			// A noul that says nothing is refused by the service, so it is
			// refused here rather than after a round trip.
			"noul with neither instructions nor criteria",
			typesafe.Questions{"q": &typesafe.NoulQuestion{}},
			`noul question "q" has neither instructions nor criteria`,
		},
		{
			"noul with criteria that describe nothing",
			typesafe.Questions{"q": &typesafe.NoulQuestion{Criteria: &typesafe.NoulCriteria{}}},
			`noul question "q" has neither instructions nor criteria`,
		},
		{
			"no choice criteria",
			typesafe.Questions{"q": &typesafe.ChoiceQuestion{}},
			`choice question "q" has no criteria`,
		},
		{"nil question", typesafe.Questions{"q": nil}, `question "q" is nil`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.SystemOne(t.Context(),
				&typesafe.SystemOneRequest{State: "s", Questions: test.questions}, nil)
			if err == nil {
				t.Fatal("SystemOne succeeded, want a validation error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
		})
	}
	if got := len(rec.Requests()); got != 0 {
		t.Errorf("%d requests were sent, want none", got)
	}
}

func TestSystemOneRequiresARequest(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse), nil)
	if _, err := client.SystemOne(t.Context(), nil, nil); err == nil {
		t.Fatal("SystemOne(nil) succeeded, want an error")
	}
	if got := len(rec.Requests()); got != 0 {
		t.Errorf("%d requests were sent for a nil request, want none", got)
	}
}

// A request answers for its own validity, so that a caller assembling one far
// from where it is sent can ask before sending, and so that SystemOne does not
// have to reach through it to its questions.
func TestSystemOneRequestValidate(t *testing.T) {
	var absent *typesafe.SystemOneRequest
	if err := absent.Validate(); err == nil {
		t.Error("a nil request reported itself valid")
	}

	noState := &typesafe.SystemOneRequest{Questions: noulQuestion("q")}
	if err := noState.Validate(); err == nil {
		t.Error("a request with no state reported itself valid")
	} else if !strings.Contains(err.Error(), "State is required") {
		t.Errorf("error = %q", err)
	}

	empty := &typesafe.SystemOneRequest{State: "s"}
	if err := empty.Validate(); err == nil {
		t.Error("a request with no questions reported itself valid")
	} else if !strings.Contains(err.Error(), "at least one question") {
		t.Errorf("error = %q", err)
	}

	good := &typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("q")}
	if err := good.Validate(); err != nil {
		t.Errorf("Validate on a good request: %v", err)
	}
}

func TestSystemOneRejectsResponseWithoutAnswers(t *testing.T) {
	for _, body := range []any{
		map[string]any{"model": "m", "usage": map[string]any{}},
		map[string]any{},
	} {
		client, _ := newTestClient(t, always(http.StatusOK, body), nil)
		_, err := client.SystemOne(t.Context(),
			&typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("q")}, nil)
		if err == nil {
			t.Errorf("SystemOne accepted %s", mustJSON(body))
			continue
		}
		if !strings.Contains(err.Error(), "carried no answers") {
			t.Errorf("error = %q", err)
		}
	}
}

// TestClientIsConcurrencySafe is meaningful under -race: one client must serve
// many goroutines, which is how the API is meant to be used.
func TestClientIsConcurrencySafe(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse),
		&typesafe.ClientOptions{Header: http.Header{"X-Trace": {"shared"}}})

	const calls = 32
	errs := make(chan error, calls)
	for i := range calls {
		go func() {
			_, err := client.SystemOne(t.Context(), &typesafe.SystemOneRequest{
				State:     i,
				Questions: noulQuestion("q"),
			}, &typesafe.RequestOptions{Header: http.Header{"X-Call": {strconv.Itoa(i)}}})
			errs <- err
		}()
	}
	for range calls {
		if err := <-errs; err != nil {
			t.Errorf("SystemOne: %v", err)
		}
	}
	if got := len(rec.Requests()); got != calls {
		t.Errorf("%d requests, want %d", got, calls)
	}
	for _, req := range rec.Requests() {
		if got := req.Header.Get("X-Trace"); got != "shared" {
			t.Errorf("a concurrent call lost the client header: %q", got)
		}
		if got := req.Header.Get("X-Call"); got == "" {
			t.Error("a concurrent call lost its own header")
		}
	}
}

func TestWireFormatPreservesNulls(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse), nil)

	_, err := client.SystemOne(t.Context(), &typesafe.SystemOneRequest{
		State: "",
		Questions: typesafe.Questions{
			"noul":   &typesafe.NoulQuestion{Criteria: &typesafe.NoulCriteria{True: "yes means this"}},
			"choice": &typesafe.ChoiceQuestion{Criteria: typesafe.ChoiceCriteria{"yes": nil, "no": nil}},
			"score":  &typesafe.ScoreQuestion{Criteria: typesafe.ScoreCriteria{nil, "high"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	want := map[string]any{
		"state": "",
		"model": typesafe.DefaultModel,
		"questions": map[string]any{
			"noul": map[string]any{
				"type": "noul", "instructions": nil,
				"criteria": map[string]any{"true": "yes means this"},
			},
			"choice": map[string]any{
				"type": "choice", "instructions": nil,
				"criteria": map[string]any{"yes": nil, "no": nil},
			},
			"score": map[string]any{
				"type": "score", "instructions": nil,
				"criteria": []any{nil, "high"},
			},
		},
	}
	if got := rec.Last(t).JSON(t); !jsonEqual(got, want) {
		t.Errorf("body =\n%s\nwant\n%s", mustJSON(got), mustJSON(want))
	}
}

func TestWireFormatStructuredEntries(t *testing.T) {
	client, rec := newTestClient(t, always(http.StatusOK, systemOneResponse), nil)

	rich := map[string]any{"summary": "warm", "examples": []any{"hi!", "welcome"}}
	_, err := client.SystemOne(t.Context(), &typesafe.SystemOneRequest{
		State: []any{nil, map[string]any{"messages": []any{"hello"}}},
		Questions: typesafe.Questions{
			"noul": &typesafe.NoulQuestion{
				Instructions: []any{nil, map[string]any{"examples": []any{1, false}}},
				Criteria:     &typesafe.NoulCriteria{True: []any{"yes", nil}},
			},
			"choice": &typesafe.ChoiceQuestion{
				Criteria: typesafe.ChoiceCriteria{"friendly": rich, "hostile": nil},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	body := rec.Last(t).JSON(t).(map[string]any)
	if !jsonEqual(body["state"], []any{nil, map[string]any{"messages": []any{"hello"}}}) {
		t.Errorf("state = %s", mustJSON(body["state"]))
	}
	questions := body["questions"].(map[string]any)
	wantNoul := map[string]any{
		"type":         "noul",
		"instructions": []any{nil, map[string]any{"examples": []any{1, false}}},
		// False was left nil, so it is omitted rather than sent as null: both
		// mean the outcome is undescribed.
		"criteria": map[string]any{"true": []any{"yes", nil}},
	}
	if !jsonEqual(questions["noul"], wantNoul) {
		t.Errorf("noul question = %s, want %s", mustJSON(questions["noul"]), mustJSON(wantNoul))
	}
	choice := questions["choice"].(map[string]any)["criteria"].(map[string]any)
	if !jsonEqual(choice["friendly"], rich) {
		t.Errorf("friendly = %s", mustJSON(choice["friendly"]))
	}
	if value, ok := choice["hostile"]; !ok || value != nil {
		t.Errorf("hostile = %v (present %v), want an explicit null", value, ok)
	}
}

func TestMetaCarriesRequestIDAndRawBody(t *testing.T) {
	header := http.Header{"X-Typesafe-Request-Id": {"req_1"}}
	client, _ := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		return jsonResponse(http.StatusOK, systemOneResponse, header), nil
	}, nil)

	result, err := client.SystemOne(t.Context(),
		&typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("q")}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if result.Meta.RequestID != "req_1" {
		t.Errorf("RequestID = %q", result.Meta.RequestID)
	}
	if result.Meta.Status != http.StatusOK {
		t.Errorf("Status = %d", result.Meta.Status)
	}
	if !jsonEqual(decode(t, result.Meta.Body), systemOneResponse) {
		t.Errorf("Meta.Body = %s", result.Meta.Body)
	}
}

// A 2xx with nothing in it leaves the result untouched, so the shape check each
// call makes is what turns it into an error a caller can read, rather than an
// empty value they have to diagnose.
func TestSuccessfulResponseWithNoBody(t *testing.T) {
	client, _ := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		return rawResponse(http.StatusOK, nil, "", nil), nil
	}, nil)

	_, err := client.ListModels(t.Context(), nil)
	if err == nil {
		t.Fatal("ListModels accepted an empty 200")
	}
	if !strings.Contains(err.Error(), "is not a model list") {
		t.Errorf("error = %q", err)
	}

	_, err = client.SystemOne(t.Context(),
		&typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("q")}, nil)
	if err == nil {
		t.Fatal("SystemOne accepted an empty 200")
	}
	if !strings.Contains(err.Error(), "carried no answers") {
		t.Errorf("error = %q", err)
	}
}

func TestPerCallTimeout(t *testing.T) {
	client, _ := newTestClient(t, hang(), nil)

	_, err := client.SystemOne(t.Context(),
		&typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("q")},
		&typesafe.RequestOptions{Timeout: 10 * time.Millisecond})

	var connErr *typesafe.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("error = %v (%T), want a *ConnectionError", err, err)
	}
	if connErr.Timeout != 10*time.Millisecond {
		t.Errorf("Timeout = %s, want 10ms", connErr.Timeout)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("a timed-out attempt does not match context.DeadlineExceeded")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q", err)
	}
}

func TestCallerCancellationIsNotAConnectionError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	client, _ := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		cancel()
		time.Sleep(50 * time.Millisecond)
		return nil, ctx.Err()
	}, nil)

	_, err := client.ListModels(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	var connErr *typesafe.ConnectionError
	if errors.As(err, &connErr) {
		t.Error("the caller's own cancellation was reported as a ConnectionError")
	}
}

func TestConnectionFailure(t *testing.T) {
	boom := errors.New("dial tcp: no route to host")
	client, _ := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		return nil, boom
	}, nil)

	_, err := client.ListModels(t.Context(), nil)
	var connErr *typesafe.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("error = %v (%T), want a *ConnectionError", err, err)
	}
	if connErr.Timeout != 0 {
		t.Errorf("Timeout = %s, want zero for a non-timeout failure", connErr.Timeout)
	}
	if !strings.Contains(err.Error(), "no route to host") {
		t.Errorf("error = %q, want it to keep the cause", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Error("a connection failure matched context.DeadlineExceeded")
	}
}

func TestPerCallOptionsAreValidated(t *testing.T) {
	client, _ := newTestClient(t, always(http.StatusOK, map[string]any{"models": []any{}}), nil)

	if _, err := client.ListModels(t.Context(), &typesafe.RequestOptions{Timeout: -1}); err == nil {
		t.Error("a negative per-call timeout was accepted")
	}
	bad := typesafe.DefaultRetryPolicy()
	bad.BackoffJitter = -1
	if _, err := client.ListModels(t.Context(), &typesafe.RequestOptions{Retry: &bad}); err == nil {
		t.Error("an invalid per-call retry policy was accepted")
	}
}

// --- helpers ---

func decode(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return value
}

// jsonEqual compares two values by their JSON encodings, which puts a decoded
// float64 and the int a test literal wrote beside each other on equal terms.
func jsonEqual(a, b any) bool {
	return string(mustJSON(a)) == string(mustJSON(b))
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
