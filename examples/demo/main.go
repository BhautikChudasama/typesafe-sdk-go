// The demo command asks a support ticket a few questions of each kind.
//
// Run it with an API key in the environment:
//
//	TYPESAFE_API_KEY=sk-... go run ./examples/demo
//
// Set TYPESAFE_LOG_LEVEL=debug to see the requests and responses.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	client, err := typesafe.NewClient(nil)
	if err != nil {
		return err
	}

	// One context for the whole program: the per-attempt timeout does not bound
	// a retried call, and this does.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	models, err := client.ListModels(ctx, nil)
	if err != nil {
		return report(err)
	}
	fmt.Println("available models:", strings.Join(models.Names(), ", "))

	ticket := map[string]any{
		"subject": "Charged twice this month",
		"body": "Hi, I see two charges of $49 on my card for August. I only have one account. " +
			"Please fix this ASAP, I'm pretty frustrated.",
	}

	result, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
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
			"refundRisk": &typesafe.ScoreQuestion{
				Instructions: "How likely is the customer to demand a refund?",
				Criteria:     typesafe.ScoreCriteria{"unlikely", "possible", "likely"},
			},
		},
	}, nil)
	if err != nil {
		return report(err)
	}

	isBilling, err := result.Answers.Noul("isBilling")
	if err != nil {
		return err
	}
	tone, err := result.Answers.Choice("tone")
	if err != nil {
		return err
	}
	urgency, err := result.Answers.Score("urgency")
	if err != nil {
		return err
	}
	refundRisk, err := result.Answers.Score("refundRisk")
	if err != nil {
		return err
	}

	fmt.Printf("model        %s\n", result.Model)
	fmt.Printf("billing?     %.2f\n", isBilling.Noul)
	fmt.Printf("tone         %s (%.2f)\n", tone.Choice, tone.Probability())
	fmt.Printf("urgency      %.2f on a 0-3 scale, nearest %q\n", urgency.Score, urgency.Description())
	fmt.Printf("refund risk  %.2f (%.2f confidence)\n", refundRisk.Score, refundRisk.Confidence)
	fmt.Printf("tokens       %d in / %d out\n", result.Usage.InputTokens, result.Usage.OutputTokens)
	return nil
}

// report turns a failed call into the message a person running this can act on.
func report(err error) error {
	var apiErr *typesafe.APIError
	switch {
	case errors.Is(err, typesafe.ErrNoAPIKey):
		return fmt.Errorf("%w (set %s)", err, typesafe.EnvAPIKey)
	case errors.Is(err, typesafe.ErrAuthentication):
		fmt.Fprintf(os.Stderr, "the API key was rejected; check %s\n", typesafe.EnvAPIKey)
		return err
	case errors.As(err, &apiErr):
		fmt.Fprintf(os.Stderr, "request %s failed: %v\n", apiErr.RequestID, apiErr.Body)
		return err
	default:
		return err
	}
}
