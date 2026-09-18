// Package typesafe provides an SDK for the TypeSafe AI API.
//
// TypeSafe answers typed questions about a piece of state and returns
// probability distributions, not prose. There is nothing to parse and no format
// to coax out of a model: a question comes back as a number your code can act
// on.
//
// To get started, create a [Client] and call [Client.SystemOne] with the state
// to evaluate and the questions to ask:
//
//	client, err := typesafe.NewClient(nil) // reads TYPESAFE_API_KEY
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	result, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
//		State: "I was charged twice. Please fix this ASAP.",
//		Questions: typesafe.Questions{
//			"category": &typesafe.ChoiceQuestion{
//				Instructions: "What is this ticket about?",
//				Criteria: typesafe.ChoiceCriteria{
//					"billing":   nil,
//					"technical": nil,
//					"other":     nil,
//				},
//			},
//		},
//	}, nil)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	category, err := result.Answers.Choice("category")
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(category.Choice, category.Confidence)
//
// # Questions and answers
//
// There are three question types, and one call may mix them freely. Each is
// evaluated in parallel and in isolation against the same state, so a question
// never sees another's answer.
//
//   - A [NoulQuestion] asks something yes-or-no and is answered with the
//     probability of yes, so that the threshold is yours to choose.
//   - A [ChoiceQuestion] selects one of a set of labels and reports a
//     probability for each.
//   - A [ScoreQuestion] rates the state against an ordered rubric and reports
//     an expected score, which may fall between two levels.
//
// Keep each question atomic. A question that asks two things at once has no
// single answer to report a probability for; ask both and combine them in your
// own code, which is cheaper to understand and to change than a longer prompt.
//
// The TypeScript SDK infers each answer's type from the question that produced
// it. Go has no equivalent for a map whose value type varies by key, so the
// answers arrive as an [Answers] map of an [Answer] interface, and
// [Answers.Noul], [Answers.Choice], and [Answers.Score] name the type you
// expect:
//
//	urgency, err := result.Answers.Score("urgency")
//
// An answer owns the readings taken from it, so that the arithmetic a caller
// would otherwise repeat has one place to be right: [ChoiceAnswer.Probability]
// for how likely the selected label was, [ScoreAnswer.Level] and
// [ScoreAnswer.Description] for where an expected score lands on the rubric.
// [NoulAnswer] has no such helper on purpose — see its documentation.
//
// # Configuration
//
// [NewClient] takes its settings from [ClientOptions], then from the
// environment, then from the SDK defaults. See [EnvAPIKey] and the constants
// beside it for the variables, and [DefaultBaseURL] and [DefaultModel] for the
// defaults. A zero [ClientOptions], or a nil one, configures a client entirely
// from the environment.
//
// # Timeouts, retries, and errors
//
// [ClientOptions.Timeout] bounds each attempt and defaults to
// [DefaultTimeout]. Retries have no budget of their own, so bound a whole call
// with its [context.Context]:
//
//	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
//	defer cancel()
//
// [DefaultRetryPolicy] retries 408, 429, and every 5xx, along with connection
// failures and timeouts, twice each, backing off exponentially with jitter and
// honoring a Retry-After header within a minute. A [RetryPolicy] is a complete
// setting rather than a patch: start from [DefaultRetryPolicy] and change what
// you need.
//
// Three kinds of error come back from a call, and [errors.Is] and [errors.As]
// tell them apart:
//
//   - An [*APIError] is a response the service refused to fulfil. Match its
//     class with [ErrRateLimit] and the sentinels beside it rather than
//     comparing status codes.
//   - A [*ConnectionError] is a request that produced no complete response,
//     including one that ran out of time; those also match
//     [context.DeadlineExceeded].
//   - The context's own error is returned when the caller gives up, so
//     [context.Canceled] means your cancellation and nothing else.
//
// # Concurrency
//
// A [Client] is safe for concurrent use and pools its connections, so create
// one for the life of the program and share it. Fanning many questions out
// across goroutines against one client is the intended way to use the API.
package typesafe
