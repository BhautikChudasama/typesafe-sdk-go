package typesafe_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	typesafe "github.com/BhautikChudasama/typesafe-sdk-go"
)

// stubService stands in for api.typesafe.ai so that the examples below run
// offline and print the same thing every time. Point a real client at
// [typesafe.DefaultBaseURL] instead, which is where a client with no BaseURL
// goes.
func stubService(answer func(request map[string]any) any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer(request))
	}))
}

func Example() {
	service := stubService(func(map[string]any) any {
		return map[string]any{
			"model": "jev-1.13",
			"answers": map[string]any{
				"category": map[string]any{
					"type": "choice", "choice": "billing", "confidence": 0.94,
					"probabilities": map[string]float64{"billing": 0.92, "technical": 0.05, "other": 0.03},
				},
			},
			"usage": map[string]any{"input_tokens": 41, "output_tokens": 3},
		}
	})
	defer service.Close()

	client, err := typesafe.NewClient(&typesafe.ClientOptions{
		APIKey:  "sk-example",
		BaseURL: service.URL,
	})
	if err != nil {
		log.Fatal(err)
	}

	result, err := client.SystemOne(context.Background(), &typesafe.SystemOneRequest{
		State: "I was charged twice. Please fix this ASAP.",
		Questions: typesafe.Questions{
			"category": &typesafe.ChoiceQuestion{
				Instructions: "What is this ticket about?",
				Criteria: typesafe.ChoiceCriteria{
					"billing":   nil,
					"technical": nil,
					"other":     nil,
				},
			},
		},
	}, nil)
	if err != nil {
		log.Fatal(err)
	}

	category, err := result.Answers.Choice("category")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s (%.2f confidence)\n", category.Choice, category.Confidence)
	// Output:
	// billing (0.94 confidence)
}

// A single call may mix all three question types. They are evaluated in
// parallel and in isolation, so keep each one atomic and combine the answers in
// your own code rather than in a longer instruction.
func ExampleClient_SystemOne() {
	service := stubService(func(map[string]any) any {
		return map[string]any{
			"model": "jev-1.13",
			"answers": map[string]any{
				"isBilling": map[string]any{"type": "noul", "noul": 0.97},
				"tone": map[string]any{
					"type": "choice", "choice": "frustrated", "confidence": 0.81,
					"probabilities": map[string]float64{"calm": 0.04, "frustrated": 0.79, "angry": 0.17},
				},
				"urgency": map[string]any{
					"type": "score", "score": 2.6, "confidence": 0.7,
					"legend":        map[string]any{"0": "can wait", "1": "this week", "2": "today", "3": "right now"},
					"probabilities": map[string]float64{"0": 0.02, "1": 0.08, "2": 0.18, "3": 0.72},
				},
			},
			"usage": map[string]any{"input_tokens": 88, "output_tokens": 9},
		}
	})
	defer service.Close()

	client, err := typesafe.NewClient(&typesafe.ClientOptions{APIKey: "sk-example", BaseURL: service.URL})
	if err != nil {
		log.Fatal(err)
	}

	ticket := map[string]any{
		"subject": "Charged twice this month",
		"body":    "I see two charges of $49 on my card for August. Please fix this ASAP.",
	}
	result, err := client.SystemOne(context.Background(), &typesafe.SystemOneRequest{
		State: ticket,
		Questions: typesafe.Questions{
			"isBilling": &typesafe.NoulQuestion{Instructions: "Is this ticket about billing?"},
			"tone": &typesafe.ChoiceQuestion{
				Instructions: "What is the customer's tone?",
				Criteria:     typesafe.ChoiceCriteria{"calm": nil, "frustrated": nil, "angry": nil},
			},
			"urgency": &typesafe.ScoreQuestion{
				Instructions: "How urgent is this ticket?",
				Criteria:     typesafe.ScoreCriteria{"can wait", "this week", "today", "right now"},
			},
		},
	}, nil)
	if err != nil {
		log.Fatal(err)
	}

	isBilling, err := result.Answers.Noul("isBilling")
	if err != nil {
		log.Fatal(err)
	}
	tone, err := result.Answers.Choice("tone")
	if err != nil {
		log.Fatal(err)
	}
	urgency, err := result.Answers.Score("urgency")
	if err != nil {
		log.Fatal(err)
	}

	// A noul is a probability, not a verdict: the threshold is yours.
	fmt.Printf("billing:  %.2f (route it: %v)\n", isBilling.Noul, isBilling.Noul > 0.8)
	fmt.Printf("tone:     %s at p=%.2f\n", tone.Choice, tone.Probability())
	fmt.Printf("urgency:  %.1f of 3, nearest %q\n", urgency.Score, urgency.Description())
	fmt.Printf("tokens:   %d in, %d out\n", result.Usage.InputTokens, result.Usage.OutputTokens)
	// Output:
	// billing:  0.97 (route it: true)
	// tone:     frustrated at p=0.79
	// urgency:  2.6 of 3, nearest "right now"
	// tokens:   88 in, 9 out
}

func ExampleClient_ListModels() {
	service := stubService(func(map[string]any) any {
		return map[string]any{"models": []any{
			map[string]any{"name": "jev-1.13", "description": "general purpose", "release_date": "2026-07-01"},
			map[string]any{"name": "jev-latest", "description": "alias for the newest jev", "release_date": "2026-07-01"},
		}}
	})
	defer service.Close()

	client, err := typesafe.NewClient(&typesafe.ClientOptions{APIKey: "sk-example", BaseURL: service.URL})
	if err != nil {
		log.Fatal(err)
	}

	list, err := client.ListModels(context.Background(), nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(strings.Join(list.Names(), ", "))
	for _, model := range list.Models {
		fmt.Printf("%s: %s\n", model.Name, model.Description)
	}
	// Output:
	// jev-1.13, jev-latest
	// jev-1.13: general purpose
	// jev-latest: alias for the newest jev
}

// Match a failure by its class rather than by its status code. The request ID
// is worth logging: it is what identifies the call in the service's own logs.
func ExampleAPIError() {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Typesafe-Request-Id", "req_01HQ")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"message":"rate limit exceeded"}}`)
	}))
	defer service.Close()

	// Retrying is on by default; it is off here so the example does not wait.
	noRetries := typesafe.DefaultRetryPolicy()
	noRetries.MaxRetries = 0
	client, err := typesafe.NewClient(&typesafe.ClientOptions{
		APIKey: "sk-example", BaseURL: service.URL, Retry: &noRetries,
	})
	if err != nil {
		log.Fatal(err)
	}

	_, err = client.ListModels(context.Background(), nil)
	switch {
	case errors.Is(err, typesafe.ErrRateLimit):
		var apiErr *typesafe.APIError
		errors.As(err, &apiErr)
		fmt.Printf("rate limited (request %s): %s\n", apiErr.RequestID, err)
	case errors.Is(err, typesafe.ErrAuthentication):
		fmt.Println("check TYPESAFE_API_KEY")
	case err != nil:
		fmt.Println("failed:", err)
	}
	// Output:
	// rate limited (request req_01HQ): typesafe: 429 rate limit exceeded
}

// A RetryPolicy is a complete setting, not a patch: start from the defaults and
// change what you need, so that a zero field is never mistaken for an unset one.
func ExampleRetryPolicy() {
	policy := typesafe.DefaultRetryPolicy()
	policy.MaxRetries = 5
	policy.HTTPStatuses = append(policy.HTTPStatuses, http.StatusConflict)

	client, err := typesafe.NewClient(&typesafe.ClientOptions{APIKey: "sk-example", Retry: &policy})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(client.Retry().MaxRetries, client.Retry().RetriesStatus(http.StatusConflict))
	// Output:
	// 5 true
}
