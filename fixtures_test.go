package typesafe_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

// The payloads under testdata are responses the live service actually sent,
// captured once and replayed here. A fixture somebody wrote by hand only proves
// the SDK agrees with whoever wrote it; these prove it agrees with the service,
// and they keep proving it without spending a request per run.
//
// Regenerate one by replacing the file with a fresh response body. A test that
// then fails is telling you the service changed its shape.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	//nolint:gosec // G304: the path is a fixture name from this package's own tests
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	return raw
}

// serving returns a client that answers every request with one fixture.
func serving(t *testing.T, status int, name string, header http.Header) *typesafe.Client {
	t.Helper()
	body := fixture(t, name)
	client, _ := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		return rawResponse(status, body, "application/json", header), nil
	}, nil)
	return client
}

func TestRealModelsPayload(t *testing.T) {
	client := serving(t, http.StatusOK, "models.json", nil)

	list, err := client.ListModels(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(list.Models) == 0 {
		t.Fatal("the service's own response decoded to no models")
	}
	if !slices.Contains(list.Names(), typesafe.DefaultModel) {
		t.Errorf("Names() = %v, want it to include the SDK default %q", list.Names(), typesafe.DefaultModel)
	}
	for _, model := range list.Models {
		if model.Name == "" || model.Description == "" || model.ReleaseDate == "" {
			t.Errorf("a field of %+v did not decode; the wire names may have changed", model)
		}
	}
}

func TestRealSystemOnePayload(t *testing.T) {
	client := serving(t, http.StatusOK, "systemone.json",
		http.Header{"X-Typesafe-Request-Id": {"req_01a0b3aa544774e69caeeae7f7e2dae5"}})

	result, err := client.SystemOne(t.Context(), &typesafe.SystemOneRequest{
		State: "I was charged twice, please fix this ASAP.",
		Questions: typesafe.Questions{
			"isBilling": &typesafe.NoulQuestion{Instructions: "Is this about billing?"},
			"tone": &typesafe.ChoiceQuestion{
				Instructions: "Tone?",
				Criteria:     typesafe.ChoiceCriteria{"calm": nil, "frustrated": nil, "angry": nil},
			},
			"urgency": &typesafe.ScoreQuestion{
				Instructions: "How urgent?",
				Criteria:     typesafe.ScoreCriteria{"can wait", "this week", "today", "right now"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	// The service resolves the alias the request was sent with to the release
	// that answered it, which is why Model is worth reading back.
	if result.Model == typesafe.DefaultModel || result.Model == "" {
		t.Errorf("Model = %q, want the release the alias resolved to", result.Model)
	}
	if result.Usage.InputTokens <= 0 || result.Usage.OutputTokens <= 0 {
		t.Errorf("Usage = %+v, want both counts decoded", result.Usage)
	}
	if result.Meta.RequestID == "" {
		t.Error("Meta.RequestID did not decode from the response header")
	}

	noul, err := result.Answers.Noul("isBilling")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	if noul.Noul != 0.98 {
		t.Errorf("Noul = %v, want the 0.98 the service sent", noul.Noul)
	}

	tone, err := result.Answers.Choice("tone")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if tone.Choice != "frustrated" {
		t.Errorf("Choice = %q", tone.Choice)
	}
	if got := tone.Probability(); got != 0.9 {
		t.Errorf("Probability() = %v, want 0.9", got)
	}
	if tone.Confidence == tone.Probability() {
		t.Error("the service's own payload no longer distinguishes confidence from probability")
	}
	if total := sum(tone.Probabilities); !near(total, 1, 0.01) {
		t.Errorf("the choice probabilities sum to %v, want 1", total)
	}

	urgency, err := result.Answers.Score("urgency")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if urgency.Score != 2.82 {
		t.Errorf("Score = %v, want 2.82", urgency.Score)
	}
	// The wire keys the rubric by a numeric string; these assertions are what
	// prove the map[int] decoding is right against a real payload.
	if urgency.Level() != 3 {
		t.Errorf("Level() = %d, want 3", urgency.Level())
	}
	if got := urgency.Description(); got != "right now" {
		t.Errorf("Description() = %v, want %q", got, "right now")
	}
	if got := urgency.Legend[0]; got != "can wait" {
		t.Errorf("Legend[0] = %v, want the rubric echoed back", got)
	}
	if got := urgency.Probabilities[3]; got != 0.82 {
		t.Errorf("Probabilities[3] = %v, want 0.82", got)
	}
	if total := sum(urgency.Probabilities); !near(total, 1, 0.01) {
		t.Errorf("the score probabilities sum to %v, want 1", total)
	}
}

// TestRealErrorPayloads is the reason messageFromBody has the branches it has:
// each of these is a shape the service actually produces.
func TestRealErrorPayloads(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		fixture string
		class   error
		message string
	}{
		{
			name: "authentication", status: http.StatusUnauthorized,
			fixture: "error_401_authentication.json", class: typesafe.ErrAuthentication,
			message: "typesafe: 401 Cannot authenticate with the server. Please check your API key and try again.",
		},
		{
			name: "unknown model", status: http.StatusBadRequest,
			fixture: "error_400_unknown_model.json", class: typesafe.ErrBadRequest,
			message: "typesafe: 400 Unknown model: no-such-model",
		},
		{
			name: "request validation", status: http.StatusUnprocessableEntity,
			fixture: "error_422_validation.json", class: typesafe.ErrUnprocessableEntity,
			message: "typesafe: 422 questions.q.score.criteria: Input should be a valid list",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := serving(t, test.status, test.fixture, nil)

			_, err := client.ListModels(t.Context(), nil)
			if err == nil {
				t.Fatal("ListModels accepted an error response")
			}
			if !errors.Is(err, test.class) {
				t.Errorf("error = %v, want it to match %v", err, test.class)
			}
			if got := err.Error(); got != test.message {
				t.Errorf("Error() = %q, want %q", got, test.message)
			}
			var apiErr *typesafe.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != test.status {
				t.Errorf("errors.As did not recover a %d", test.status)
			}
			// Whatever the SDK made of the message, the parsed body stays
			// reachable for a caller who needs the part it did not use.
			body, ok := apiErr.Body.(map[string]any)
			if !ok || body["detail"] == nil {
				t.Errorf("Body = %#v, want the service's own object", apiErr.Body)
			}
		})
	}
}

// --- helpers ---

func sum[K comparable](values map[K]float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total
}

func near(got, want, tolerance float64) bool {
	diff := got - want
	return diff < tolerance && diff > -tolerance
}
