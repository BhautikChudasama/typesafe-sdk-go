# typesafe-sdk-go

[![Go Reference](https://pkg.go.dev/badge/github.com/Tangerg/typesafe-sdk-go.svg)](https://pkg.go.dev/github.com/Tangerg/typesafe-sdk-go)
[![Go 1.25+](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![MIT](https://img.shields.io/badge/licence-MIT-blue)](./LICENSE)

`typesafe-sdk-go` is a Go SDK for the [TypeSafe AI](https://typesafe.ai) API,
ported from the
[JavaScript SDK](https://github.com/typesafe-ai/typesafe-sdk-js) and speaking the
same wire protocol.

TypeSafe answers typed questions about a piece of state and returns probability
distributions, not prose. There is nothing to parse and no format to coax out of
a model.

Install the module with:

```sh
go get github.com/Tangerg/typesafe-sdk-go@latest
```

It needs Go 1.25 or newer and has no third-party dependencies.

The package is named `typesafe`, so an import needs no alias in most editors but
reads better with one:

```go
import typesafe "github.com/Tangerg/typesafe-sdk-go"
```

## Quickstart

Set `TYPESAFE_API_KEY` in your environment, then:

```go
package main

import (
	"context"
	"fmt"
	"log"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

func main() {
	client, err := typesafe.NewClient(nil) // reads TYPESAFE_API_KEY
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
	fmt.Println(category.Choice, category.Confidence)
}
```

There is a runnable version in [`examples/demo`](examples/demo):

```sh
TYPESAFE_API_KEY=sk-... go run ./examples/demo
```

## Questions and answers

Three question types, mixable in one call. Each is evaluated in parallel and in
isolation against the same state, so a question never sees another's answer.
Keep each one atomic and combine them in your own code.

| Question          | Asks                              | Answer                                          |
| ----------------- | --------------------------------- | ----------------------------------------------- |
| `NoulQuestion`    | something yes-or-no               | `Noul` — the probability of yes                 |
| `ChoiceQuestion`  | which of these labels             | `Choice`, `Confidence`, `Probabilities`          |
| `ScoreQuestion`   | where on this rubric              | `Score`, `Confidence`, `Legend`, `Probabilities` |

```go
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
	},
}, nil)
```

A `ChoiceCriteria` value of `nil` leaves a label undescribed, which is the right
choice when the label speaks for itself. A `ScoreCriteria` is a list, and its
order is its meaning: position zero is the lowest score.

The service's own rules are checked before a request is sent, so they cost no
round trip:

| Rule | |
| --- | --- |
| `State` is required | nil is refused; `""`, `{}` and `[]` are states |
| A question name may not be empty | it is the key an answer comes back under |
| A `NoulQuestion` needs `Instructions` or `Criteria` | either may carry the question; neither is refused |
| `ChoiceCriteria` | 1 to 255 labels |
| `ScoreCriteria` | 2 to 10 levels |

Call `Validate()` on a `SystemOneRequest` or on `Questions` to check them
yourself when a request is assembled far from where it is sent.

The TypeScript SDK infers each answer's type from the question that produced it.
Go has no equivalent for a map whose value type varies by key, so the type is
named at the point of use:

```go
isBilling, err := result.Answers.Noul("isBilling")   // *NoulAnswer
tone, err := result.Answers.Choice("tone")           // *ChoiceAnswer
urgency, err := result.Answers.Score("urgency")      // *ScoreAnswer
```

Each accessor reports a clear error when the answer is absent or has a different
type than you asked for.

An answer carries the readings you would otherwise compute from it:

```go
if isBilling.Noul > 0.8 {        // a noul is a probability; the threshold is yours
	route(ticket)
}
tone.Choice                      // the selected label
tone.Probability()               // how likely that label was — not Confidence
urgency.Score                    // expected score, which may fall between levels
urgency.Level()                  // the rubric level it rounds to
urgency.Description()            // what the rubric says about that level
```

`NoulAnswer` has no `Yes()` on purpose: a threshold depends on what a wrong yes
and a wrong no cost you, and that is the one part of the decision this SDK
cannot know.

## Configuration

`NewClient` takes its settings from `ClientOptions`, then from the environment,
then from the SDK defaults. A blank environment value counts as unset.

| Option         | Environment              | Default                    |
| -------------- | ------------------------ | -------------------------- |
| `APIKey`       | `TYPESAFE_API_KEY`       | required                   |
| `BaseURL`      | `TYPESAFE_BASE_URL`      | `https://api.typesafe.ai`  |
| `DefaultModel` | `TYPESAFE_DEFAULT_MODEL` | `jev-latest`               |
| `Logger`       | `TYPESAFE_LOG_LEVEL`     | no logging                 |
| `Timeout`      | —                        | 10s per attempt            |
| `Retry`        | —                        | `DefaultRetryPolicy()`     |
| `Header`       | —                        | none                       |
| `HTTPClient`   | —                        | `http.DefaultClient`       |

A `Client` is safe for concurrent use and pools its connections. Create one for
the life of the program and share it.

## Timeouts and retries

`Timeout` bounds each attempt, not the call as a whole. Retries have no budget
of their own, so bound a whole call with its context:

```go
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
```

`DefaultRetryPolicy()` retries 408, 429, and every 5xx, along with connection
failures and timeouts, twice each: exponential backoff from 500ms to 5s with up
to 25% jitter, honoring a `Retry-After` header within a minute.

A `RetryPolicy` is a complete setting rather than a patch, so a zero field is
never mistaken for an unset one. Start from the defaults and change what you
need:

```go
policy := typesafe.DefaultRetryPolicy()
policy.MaxRetries = 5
client, err := typesafe.NewClient(&typesafe.ClientOptions{Retry: &policy})
```

A nil `Retry` in `ClientOptions` or `RequestOptions` inherits the level above it;
a non-nil one replaces it outright.

## Errors

```go
result, err := client.SystemOne(ctx, request, nil)
switch {
case errors.Is(err, typesafe.ErrRateLimit):
	// back off and try later
case errors.Is(err, typesafe.ErrAuthentication):
	// check TYPESAFE_API_KEY
case errors.Is(err, context.Canceled):
	// the caller gave up
case err != nil:
	var apiErr *typesafe.APIError
	if errors.As(err, &apiErr) {
		log.Printf("request %s failed: %v", apiErr.RequestID, apiErr.Body)
	}
}
```

- `*APIError` — a response the service refused to fulfil. Match its class with
  `ErrBadRequest`, `ErrAuthentication`, `ErrPermissionDenied`, `ErrNotFound`,
  `ErrUnprocessableEntity`, `ErrRateLimit`, and `ErrServerError` (any 5xx)
  rather than comparing status codes.
- `*ConnectionError` — a request that produced no complete response. One that
  ran out of time also matches `context.DeadlineExceeded`.
- The context's own error when the caller gives up, so `context.Canceled` means
  your cancellation and nothing else.

## Logging

Pass an `*slog.Logger` to log activity: summaries at info, full headers and
bodies at debug. Credential headers are redacted; bodies are not, and yours may
hold the data you are evaluating.

```go
logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
client, err := typesafe.NewClient(&typesafe.ClientOptions{Logger: logger})
```

With no logger the SDK is silent, unless `TYPESAFE_LOG_LEVEL` is set — which
exists so a deployed program can be made to explain itself without a rebuild.

## Raw responses

Every result carries a `Meta` with the request ID, status, headers, and the raw
response body. The typed fields drop any property the SDK does not model, and
`Meta.Body` is where to find one:

```go
fmt.Println(result.Meta.RequestID)
fmt.Println(string(list.Meta.Body))
```

To send a request property the SDK does not model yet, use
`SystemOneRequest.Extra`.

## Testing

Unit tests need no credentials and never reach the network:

```sh
go test ./...
```

They run against payloads the live service actually sent, captured under
[`testdata/`](testdata). A fixture that stops matching is the service telling you
its shape changed; refresh the file and read the diff.

Integration tests call the real API, so they are behind a build tag and cost
real requests:

```sh
export TYPESAFE_API_KEY=sk-...
go test -tags integration -v ./...
```

They assert what the SDK must get right — a probability in range, a
distribution that sums to one, a label the question offered, the caller's
cancellation coming back as theirs — not what the model happens to answer.

Keep a local key out of the repository. `.env` is gitignored for the purpose:

```sh
echo 'export TYPESAFE_API_KEY=sk-...' > .env && chmod 600 .env
. ./.env && go test -tags integration ./...
```

## Differences from the JavaScript SDK

The wire protocol is identical: the same endpoints, request bodies, headers, and
retry arithmetic. The API shape follows Go instead of TypeScript.

| JavaScript                              | Go                                                        | Why                                                                                      |
| --------------------------------------- | --------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `APIPromise`, `withResponse()`          | `(result, error)` with `result.Meta`                       | Go has no promises; the metadata a caller wants is the request ID, status, and raw body.  |
| `AbortSignal`, `APIUserAbortError`      | `context.Context`, `context.Canceled`                      | Cancellation belongs to the context, and `errors.Is` already answers the question.        |
| `APITimeoutError extends APIConnectionError` | `*ConnectionError` with a `Timeout` field              | One failure — no answer arrived. A timeout also matches `context.DeadlineExceeded`.        |
| error subclasses, `instanceof`          | `*APIError` plus class sentinels, `errors.Is`              | Go matches errors by value, not by class.                                                 |
| `noul()`, `choice()`, `score()` builders | `&NoulQuestion{…}`, `&ChoiceQuestion{…}`, `&ScoreQuestion{…}` | The builders existed to check at run time what Go's types check at compile time.       |
| answer types inferred per question       | `Answers.Noul`/`Choice`/`Score`                            | Go cannot vary a map's value type by key, so the expected type is named at the call site. |
| `Partial<RetryPolicy>` merging          | a whole `*RetryPolicy`, nil to inherit                     | Go zero values cannot express "unset"; a complete value needs no third state.             |
| `Logger` interface, `logLevel`          | `*slog.Logger`                                             | `slog` is the standard, and it owns leveling.                                             |
| `fetch`                                 | `*http.Client`                                             | Same idea, standard tool.                                                                  |
| `dangerouslyAllowBrowser`               | —                                                          | There is no browser to guard against.                                                      |

Unrecognized payloads are kept rather than dropped: an answer type this release
does not model arrives as `*UnknownAnswer` instead of failing the whole response,
and `Meta.Body` holds the raw JSON.

## Documentation

- [API reference on pkg.go.dev](https://pkg.go.dev/github.com/Tangerg/typesafe-sdk-go)
- [TypeSafe documentation](https://docs.typesafe.ai/)

## License

MIT. See [LICENSE](LICENSE).
