//go:build integration

// Package typesafe_test's integration tests call the live TypeSafe API.
//
// They are behind a build tag because they cost real requests and need a real
// key, so `go test ./...` never reaches the network. Run them with:
//
//	TYPESAFE_API_KEY=sk-... go test -tags integration -v ./...
//
// Assertions are about the SDK, not about the model: what a question is
// answered is the service's business and varies between runs, so these check
// the invariants a well-formed answer must hold — a probability in range, a
// distribution that sums to one, a label the question offered — plus one loose
// sanity check that the answers are about the state and not noise.
package typesafe_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	typesafe "github.com/BhautikChudasama/typesafe-sdk-go"
)

// liveClient returns a client pointed at the real API, or skips the test when
// no key is configured.
func liveClient(t *testing.T, options *typesafe.ClientOptions) *typesafe.Client {
	t.Helper()
	if os.Getenv(typesafe.EnvAPIKey) == "" {
		t.Skipf("set %s to run the integration tests", typesafe.EnvAPIKey)
	}
	var opts typesafe.ClientOptions
	if options != nil {
		opts = *options
	}
	client, err := typesafe.NewClient(&opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// liveContext bounds a whole call, which the per-attempt timeout does not.
func liveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ticket is the state the answer assertions below are written against.
var ticket = map[string]any{
	"subject": "Charged twice this month",
	"body": "I see two charges of $49 on my card for August. I only have one account. " +
		"Please fix this ASAP, I'm pretty frustrated.",
}

func supportQuestions() typesafe.Questions {
	return typesafe.Questions{
		"isBilling": &typesafe.NoulQuestion{Instructions: "Is this ticket about billing?"},
		"tone": &typesafe.ChoiceQuestion{
			Instructions: "What is the customer's tone?",
			Criteria:     typesafe.ChoiceCriteria{"calm": nil, "frustrated": nil, "angry": nil},
		},
		"urgency": &typesafe.ScoreQuestion{
			Instructions: "How urgent is this ticket?",
			Criteria:     typesafe.ScoreCriteria{"can wait", "this week", "today", "right now"},
		},
	}
}

func TestLiveListModels(t *testing.T) {
	client := liveClient(t, nil)

	list, err := client.ListModels(liveContext(t), nil)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(list.Models) == 0 {
		t.Fatal("the account has no models")
	}
	for _, model := range list.Models {
		if model.Name == "" {
			t.Errorf("a model came back unnamed: %+v", model)
		}
	}
	if list.Meta.RequestID == "" {
		t.Error("the response carried no x-typesafe-request-id")
	}
	if list.Meta.Status != 200 {
		t.Errorf("Meta.Status = %d", list.Meta.Status)
	}
	t.Logf("models: %v (request %s)", list.Names(), list.Meta.RequestID)
}

func TestLiveSystemOne(t *testing.T) {
	client := liveClient(t, nil)
	questions := supportQuestions()

	result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
		State:     ticket,
		Questions: questions,
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	if len(result.Answers) != len(questions) {
		t.Errorf("got %d answers for %d questions", len(result.Answers), len(questions))
	}
	// The request named an alias; the response names the release that answered.
	if result.Model == "" {
		t.Error("the response named no model")
	}
	if result.Usage.InputTokens <= 0 || result.Usage.OutputTokens <= 0 {
		t.Errorf("Usage = %+v", result.Usage)
	}
	if result.Meta.RequestID == "" {
		t.Error("the response carried no x-typesafe-request-id")
	}

	noul, err := result.Answers.Noul("isBilling")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	if noul.Noul < 0 || noul.Noul > 1 {
		t.Errorf("Noul = %v, want a probability", noul.Noul)
	}
	// Loose on purpose: this checks the answer is about the ticket, not that
	// the model reaches a particular number.
	if noul.Noul < 0.5 {
		t.Errorf("Noul = %v for a ticket about a double charge, want it above even", noul.Noul)
	}

	tone, err := result.Answers.Choice("tone")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	criteria := questions["tone"].(*typesafe.ChoiceQuestion).Criteria
	if _, offered := criteria[tone.Choice]; !offered {
		t.Errorf("Choice = %q, which the question did not offer", tone.Choice)
	}
	if len(tone.Probabilities) != len(criteria) {
		t.Errorf("got %d probabilities for %d labels", len(tone.Probabilities), len(criteria))
	}
	assertDistribution(t, "tone", tone.Probabilities)
	if got := tone.Probability(); got == 0 {
		t.Error("the selected label had no probability")
	}
	if tone.Confidence < 0 || tone.Confidence > 1 {
		t.Errorf("Confidence = %v, want a probability", tone.Confidence)
	}

	urgency, err := result.Answers.Score("urgency")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	rubric := questions["urgency"].(*typesafe.ScoreQuestion).Criteria
	if urgency.Score < 0 || urgency.Score > float64(len(rubric)-1) {
		t.Errorf("Score = %v, want it within a rubric of %d levels", urgency.Score, len(rubric))
	}
	if len(urgency.Legend) != len(rubric) {
		t.Errorf("Legend has %d levels, want the %d the rubric sent", len(urgency.Legend), len(rubric))
	}
	for level, description := range rubric {
		if got := urgency.Legend[level]; got != description {
			t.Errorf("Legend[%d] = %v, want the rubric's %v", level, got, description)
		}
	}
	if urgency.Description() != urgency.Legend[urgency.Level()] {
		t.Error("Description() disagrees with Legend at Level()")
	}
	assertDistribution(t, "urgency", urgency.Probabilities)

	t.Logf("billing=%.2f tone=%s(%.2f) urgency=%.2f %q model=%s tokens=%d/%d request=%s",
		noul.Noul, tone.Choice, tone.Probability(), urgency.Score, urgency.Description(),
		result.Model, result.Usage.InputTokens, result.Usage.OutputTokens, result.Meta.RequestID)
}

// TestLiveEntryShapes sends the three things the API accepts as state and as a
// description: text, a JSON object, and a JSON array.
func TestLiveEntryShapes(t *testing.T) {
	client := liveClient(t, nil)

	states := map[string]typesafe.Entry{
		"text":   "The parcel arrived three days late and the box was crushed.",
		"object": map[string]any{"status": "late", "damage": "crushed box"},
		"array":  []any{"arrived late", map[string]any{"damage": true}},
	}
	for name, state := range states {
		t.Run(name, func(t *testing.T) {
			result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
				State: state,
				Questions: typesafe.Questions{
					"complaint": &typesafe.NoulQuestion{
						Instructions: []any{"Is this a complaint?", map[string]any{"note": "judge the state as a whole"}},
						Criteria: &typesafe.NoulCriteria{
							True:  "the customer is reporting a problem",
							False: nil,
						},
					},
				},
			}, nil)
			if err != nil {
				t.Fatalf("SystemOne: %v", err)
			}
			answer, err := result.Answers.Noul("complaint")
			if err != nil {
				t.Fatalf("Noul: %v", err)
			}
			if answer.Noul < 0 || answer.Noul > 1 {
				t.Errorf("Noul = %v, want a probability", answer.Noul)
			}
			t.Logf("%s state → complaint=%.2f", name, answer.Noul)
		})
	}
}

func TestLiveModelOverride(t *testing.T) {
	client := liveClient(t, nil)
	ctx := liveContext(t)

	list, err := client.ListModels(ctx, nil)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	result, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
		State:     "The build is broken on main.",
		Model:     list.Models[0].Name,
		Questions: typesafe.Questions{"urgent": &typesafe.NoulQuestion{Instructions: "Is this urgent?"}},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if result.Model == "" {
		t.Error("the response named no model")
	}
	t.Logf("asked %s, answered by %s", list.Models[0].Name, result.Model)
}

func TestLiveAuthenticationError(t *testing.T) {
	if os.Getenv(typesafe.EnvAPIKey) == "" {
		t.Skipf("set %s to run the integration tests", typesafe.EnvAPIKey)
	}
	// A rejected key is not worth retrying, and retrying would only slow the
	// test down.
	noRetries := typesafe.DefaultRetryPolicy()
	noRetries.MaxRetries = 0
	client, err := typesafe.NewClient(&typesafe.ClientOptions{ //nolint:gosec // G101: the key below is deliberately not one
		APIKey: "sk-not-a-real-key",
		Retry:  &noRetries,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.ListModels(liveContext(t), nil)
	if !errors.Is(err, typesafe.ErrAuthentication) {
		t.Fatalf("error = %v, want ErrAuthentication", err)
	}
	var apiErr *typesafe.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError", err)
	}
	if apiErr.Status != 401 {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
	t.Logf("rejected as expected: %v", err)
}

func TestLiveUnknownModel(t *testing.T) {
	client := liveClient(t, nil)

	_, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
		State:     "anything",
		Model:     "no-such-model",
		Questions: typesafe.Questions{"q": &typesafe.NoulQuestion{Instructions: "?"}},
	}, nil)
	if !errors.Is(err, typesafe.ErrBadRequest) {
		t.Fatalf("error = %v, want ErrBadRequest", err)
	}
	t.Logf("refused as expected: %v", err)
}

// TestLiveCancellation checks that the caller's own cancellation comes back as
// theirs, and not dressed up as a transport failure.
func TestLiveCancellation(t *testing.T) {
	client := liveClient(t, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := client.ListModels(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	var connErr *typesafe.ConnectionError
	if errors.As(err, &connErr) {
		t.Error("the caller's cancellation was reported as a ConnectionError")
	}
}

// TestLiveTimeout checks the per-attempt timeout against a real connection. A
// millisecond is short enough that no round trip can beat it.
func TestLiveTimeout(t *testing.T) {
	noRetries := typesafe.DefaultRetryPolicy()
	noRetries.MaxRetries = 0
	client := liveClient(t, &typesafe.ClientOptions{Timeout: time.Millisecond, Retry: &noRetries})

	_, err := client.ListModels(liveContext(t), nil)
	var connErr *typesafe.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("error = %v, want a *ConnectionError", err)
	}
	if connErr.Timeout != time.Millisecond {
		t.Errorf("Timeout = %s, want 1ms", connErr.Timeout)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("a timed-out attempt does not match context.DeadlineExceeded")
	}
}

// TestLiveConcurrentFanOut is the use the documentation recommends: one client,
// many questions in flight at once.
func TestLiveConcurrentFanOut(t *testing.T) {
	client := liveClient(t, nil)
	ctx := liveContext(t)

	states := []string{
		"I was charged twice this month.",
		"How do I reset my password?",
		"Your product is terrible and I want a refund.",
		"Thanks for the quick fix yesterday!",
	}

	var wg sync.WaitGroup
	results := make([]float64, len(states))
	errs := make([]error, len(states))
	for i, state := range states {
		wg.Go(func() {
			result, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
				State:     state,
				Questions: typesafe.Questions{"negative": &typesafe.NoulQuestion{Instructions: "Is the customer unhappy?"}},
			}, nil)
			if err != nil {
				errs[i] = err
				return
			}
			answer, err := result.Answers.Noul("negative")
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = answer.Noul
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("state %d: %v", i, err)
		}
	}
	if t.Failed() {
		return
	}
	for i, got := range results {
		t.Logf("%.2f  %s", got, states[i])
	}
	// The last state is the only friendly one, so it should not read as the
	// unhappiest. This checks the answers tracked their own request rather than
	// being crossed between goroutines.
	for i := range len(states) - 1 {
		if results[len(states)-1] > results[i] {
			t.Errorf("the thank-you note scored %.2f, above %q at %.2f",
				results[len(states)-1], states[i], results[i])
		}
	}
}

// rawPost sends a body the SDK would refuse to build, so that a test can ask
// the service directly whether a limit this package encodes is still its limit.
// A 200 here means the service relaxed a rule and the constant in questions.go
// is now stricter than the API.
func rawPost(t *testing.T, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(liveContext(t), http.MethodPost,
		typesafe.DefaultBaseURL+"/v1/systemone", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+os.Getenv(typesafe.EnvAPIKey))
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("sending the request: %v", err)
	}
	defer response.Body.Close() //nolint:errcheck // a test's own read
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	return response.StatusCode, string(raw)
}

// TestLiveDocumentedLimits pins the numbers questions.go validates against to
// the service that owns them. Each rule is checked from both sides: the size
// the service accepts must still be accepted, and the size past it must still
// be refused. A failure here means the API moved and the constants should.
func TestLiveDocumentedLimits(t *testing.T) {
	client := liveClient(t, nil)

	t.Run("a rubric of ten levels is accepted", func(t *testing.T) {
		rubric := make(typesafe.ScoreCriteria, 10)
		for i := range rubric {
			rubric[i] = fmt.Sprintf("severity level %d", i)
		}
		result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
			State:     "The login page returns a 500 for every user.",
			Questions: typesafe.Questions{"severity": &typesafe.ScoreQuestion{Instructions: "How severe?", Criteria: rubric}},
		}, nil)
		if err != nil {
			t.Fatalf("SystemOne: %v", err)
		}
		answer, err := result.Answers.Score("severity")
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		if len(answer.Legend) != 10 {
			t.Errorf("Legend has %d levels, want 10", len(answer.Legend))
		}
	})

	t.Run("a rubric of eleven levels is still refused", func(t *testing.T) {
		levels := make([]string, 11)
		for i := range levels {
			levels[i] = fmt.Sprintf("%q", fmt.Sprintf("level %d", i))
		}
		status, body := rawPost(t, `{"model":"jev-latest","state":"s","questions":{"q":{"type":"score","instructions":"x","criteria":[`+
			strings.Join(levels, ",")+`]}}}`)
		if status == http.StatusOK {
			t.Errorf("the service now accepts 11 levels; raise maxScoreCriteria. Body: %s", body)
		}
		t.Logf("%d %s", status, body)
	})

	t.Run("255 labels are accepted", func(t *testing.T) {
		labels := make(typesafe.ChoiceCriteria, 255)
		for i := range 255 {
			labels[fmt.Sprintf("option-%d", i)] = nil
		}
		result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
			State:     "option-7",
			Questions: typesafe.Questions{"pick": &typesafe.ChoiceQuestion{Instructions: "Which option does the state name?", Criteria: labels}},
		}, nil)
		if err != nil {
			t.Fatalf("SystemOne: %v", err)
		}
		answer, err := result.Answers.Choice("pick")
		if err != nil {
			t.Fatalf("Choice: %v", err)
		}
		if len(answer.Probabilities) != 255 {
			t.Errorf("got %d probabilities, want 255", len(answer.Probabilities))
		}
		t.Logf("picked %q at %.2f", answer.Choice, answer.Probability())
	})

	t.Run("256 labels are still refused", func(t *testing.T) {
		labels := make([]string, 256)
		for i := range labels {
			labels[i] = fmt.Sprintf("%q:null", fmt.Sprintf("o%d", i))
		}
		status, body := rawPost(t, `{"model":"jev-latest","state":"s","questions":{"q":{"type":"choice","instructions":"x","criteria":{`+
			strings.Join(labels, ",")+`}}}}`)
		if status == http.StatusOK {
			t.Errorf("the service now accepts 256 labels; raise maxChoiceCriteria. Body: %s", body)
		}
		t.Logf("%d %s", status, body)
	})

	t.Run("a noul that says nothing is still refused", func(t *testing.T) {
		status, body := rawPost(t, `{"model":"jev-latest","state":"s","questions":{"q":{"type":"noul","instructions":null}}}`)
		if status == http.StatusOK {
			t.Errorf("the service now answers a noul with no question; relax NoulQuestion.validate. Body: %s", body)
		}
		t.Logf("%d %s", status, body)
	})

	t.Run("an unnamed question is still refused", func(t *testing.T) {
		status, body := rawPost(t, `{"model":"jev-latest","state":"s","questions":{"":{"type":"noul","instructions":"x"}}}`)
		if status == http.StatusOK {
			t.Errorf("the service now accepts an empty question key; relax Questions.Validate. Body: %s", body)
		}
		t.Logf("%d %s", status, body)
	})

	t.Run("a null state is still refused", func(t *testing.T) {
		status, body := rawPost(t, `{"model":"jev-latest","state":null,"questions":{"q":{"type":"noul","instructions":"x"}}}`)
		if status == http.StatusOK {
			t.Errorf("the service now accepts a null state; relax SystemOneRequest.Validate. Body: %s", body)
		}
		t.Logf("%d %s", status, body)
	})
}

// TestLiveNoulFromCriteriaAlone covers the half of the noul rule the SDK allows:
// criteria may carry the question when instructions do not.
func TestLiveNoulFromCriteriaAlone(t *testing.T) {
	client := liveClient(t, nil)

	result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
		State: "Hi there! Just wanted to say thanks.",
		Questions: typesafe.Questions{
			"greeting": &typesafe.NoulQuestion{
				Criteria: &typesafe.NoulCriteria{
					True:  "the message opens with a greeting",
					False: "the message opens with a request or a complaint",
				},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	answer, err := result.Answers.Noul("greeting")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	if answer.Noul < 0 || answer.Noul > 1 {
		t.Errorf("Noul = %v, want a probability", answer.Noul)
	}
	t.Logf("criteria-only noul → %.2f", answer.Noul)
}

// TestLiveEmptyStates covers the states the documentation calls valid but that
// look like nothing: empty text, an empty object, an empty array. Only nil is
// refused.
func TestLiveEmptyStates(t *testing.T) {
	client := liveClient(t, nil)

	for name, state := range map[string]typesafe.Entry{
		"empty string": "",
		"empty object": map[string]any{},
		"empty array":  []any{},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
				State:     state,
				Questions: typesafe.Questions{"empty": &typesafe.NoulQuestion{Instructions: "Is the state empty?"}},
			}, nil)
			if err != nil {
				t.Errorf("SystemOne: %v", err)
			}
		})
	}
}

// TestLiveScoreIsWeightedMean checks the relationship the score documentation
// states: the score is the probability-weighted mean of the levels. It is also
// the strongest available check that the rubric's numeric-string keys decoded
// onto the right integers.
func TestLiveScoreIsWeightedMean(t *testing.T) {
	client := liveClient(t, nil)

	result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
		State: "The checkout page is down for every customer and we are losing orders.",
		Questions: typesafe.Questions{
			"severity": &typesafe.ScoreQuestion{
				Instructions: "How severe is this incident?",
				Criteria:     typesafe.ScoreCriteria{"cosmetic", "minor", "major", "critical"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	answer, err := result.Answers.Score("severity")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	var weighted float64
	for level, probability := range answer.Probabilities {
		weighted += float64(level) * probability
	}
	// The service rounds what it reports, so the comparison allows for that.
	if !near(weighted, answer.Score, 0.02) {
		t.Errorf("the levels weigh out to %v but Score is %v; the rubric keys may have decoded onto the wrong integers",
			weighted, answer.Score)
	}
	t.Logf("score=%.2f weighted=%.2f level=%d %q", answer.Score, weighted, answer.Level(), answer.Description())
}

// TestLiveStructuredCriteria sends the shapes the advanced-structure page
// recommends: objects and arrays in instructions and in every kind of criteria.
func TestLiveStructuredCriteria(t *testing.T) {
	client := liveClient(t, nil)

	result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
		State: map[string]any{"ticket": "My invoice shows tax twice", "plan": "enterprise"},
		Questions: typesafe.Questions{
			"category": &typesafe.ChoiceQuestion{
				Instructions: map[string]any{
					"field": map[string]any{"name": "category", "type": "string", "description": "what the ticket is about"},
				},
				Criteria: typesafe.ChoiceCriteria{
					"billing": map[string]any{
						"what":     "charges, invoices and tax",
						"not_for":  "questions about what a plan includes",
						"examples": []any{"charged twice", "tax looks wrong"},
					},
					"product": map[string]any{"what": "how a feature behaves"},
					"other":   nil,
				},
			},
			"severity": &typesafe.ScoreQuestion{
				Instructions: []any{"How severe is this?", map[string]any{"scale": "customer impact"}},
				Criteria: typesafe.ScoreCriteria{
					map[string]any{"summary": "cosmetic", "signals": []any{"no money involved"}},
					map[string]any{"summary": "billing is wrong", "signals": []any{"the customer was overcharged"}},
				},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	category, err := result.Answers.Choice("category")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if category.Choice != "billing" {
		t.Errorf("Choice = %q, want the structured billing description to have carried", category.Choice)
	}
	severity, err := result.Answers.Score("severity")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	// The rubric was written as objects, so the legend echoes objects back.
	if _, ok := severity.Legend[0].(map[string]any); !ok {
		t.Errorf("Legend[0] = %#v, want the structured description echoed back", severity.Legend[0])
	}
	t.Logf("category=%s(%.2f) severity=%.2f", category.Choice, category.Probability(), severity.Score)
}

// TestLiveConfidenceIsDistributionShape checks what the confidence page claims:
// it is a statistic over the distribution, in 0..1, and a distribution with
// nowhere else to go is fully confident.
func TestLiveConfidenceIsDistributionShape(t *testing.T) {
	client := liveClient(t, nil)

	result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
		State:     "anything at all",
		Questions: typesafe.Questions{"only": &typesafe.ChoiceQuestion{Instructions: "Pick.", Criteria: typesafe.ChoiceCriteria{"only": nil}}},
	}, nil)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	answer, err := result.Answers.Choice("only")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if answer.Choice != "only" {
		t.Errorf("Choice = %q, want the only label offered", answer.Choice)
	}
	if !near(answer.Probability(), 1, 0.001) {
		t.Errorf("Probability() = %v, want all the mass on the only label", answer.Probability())
	}
	if !near(answer.Confidence, 1, 0.001) {
		t.Errorf("Confidence = %v, want 1 for a distribution with one outcome", answer.Confidence)
	}
}

// assertDistribution checks what any set of probabilities from the service must
// satisfy, whatever the question was.
func assertDistribution[K comparable](t *testing.T, name string, probabilities map[K]float64) {
	t.Helper()
	if len(probabilities) == 0 {
		t.Errorf("%s: no probabilities", name)
		return
	}
	var total float64
	for key, value := range probabilities {
		if value < 0 || value > 1 {
			t.Errorf("%s: probability of %v is %v, want it between 0 and 1", name, key, value)
		}
		total += value
	}
	if total < 0.99 || total > 1.01 {
		t.Errorf("%s: probabilities sum to %v, want 1", name, total)
	}
}
