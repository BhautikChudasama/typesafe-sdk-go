package typesafe

import (
	"encoding/json"
	"maps"
	"slices"
)

// An Entry is a JSON value the API accepts wherever it takes free-form content:
// the state under evaluation, a question's instructions, and the description of
// an outcome.
//
// It is a string for plain text, a map or slice for structured content, and nil
// for JSON null, which means "undescribed" wherever a description is expected.
// The alias exists to name that contract in signatures; any JSON-encodable Go
// value is accepted.
type Entry = any

// The wire discriminants of the three question and answer types. The service
// calls a yes/no question a noul.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)

// A Question is one of [*NoulQuestion], [*ChoiceQuestion], or [*ScoreQuestion].
//
// The set is closed: validate is unexported, so the three types in this package
// are the only implementations.
type Question interface {
	// QuestionType reports the wire discriminant: [TypeNoul], [TypeChoice], or
	// [TypeScore].
	QuestionType() string

	// validate reports why the question cannot be sent, naming it as the
	// request keys it. Each type owns its own rules so that adding a fourth
	// does not mean editing a switch somewhere else.
	//
	// Every implementation answers for a nil receiver first. A typed nil in a
	// [Questions] map is not the nil interface value, so it reaches here, where
	// reading a field off it would panic — and were it sent instead, it would
	// encode as a bare null and come back as a 422 far from the mistake.
	validate(name string) error
}

// Questions are the questions of one request, keyed by the names their answers
// are returned under. The names are yours; the service echoes them back in
// [SystemOneResult.Answers].
//
// Each question is evaluated in parallel and in isolation against the same
// state, so questions do not see one another's answers. Keep each one atomic
// and combine them in your own code.
type Questions map[string]Question

// A NoulQuestion asks a yes/no question and is answered with the probability of
// yes, rather than with a yes or a no. See [NoulAnswer].
type NoulQuestion struct {
	// Instructions is the question. A nil Instructions is sent as null, which
	// asks the service to judge the state against Criteria alone — so a question
	// with neither is refused, having said nothing about what to judge.
	Instructions Entry `json:"instructions"`
	// Criteria optionally describes what the two outcomes mean. A nil Criteria
	// is omitted.
	Criteria *NoulCriteria `json:"criteria,omitzero"`
}

// NoulCriteria describes the two outcomes of a [NoulQuestion]. Either side may
// be left nil, which omits it and leaves that outcome undescribed.
type NoulCriteria struct {
	// True describes the yes outcome.
	True Entry `json:"true,omitzero"`
	// False describes the no outcome.
	False Entry `json:"false,omitzero"`
}

// describes reports whether the criteria say anything about either outcome.
//
// Absent, empty, and described-as-null are one state to the service: it refuses
// a noul whose criteria describe nothing and whose instructions are nil, rather
// than answering a question nobody asked.
func (c *NoulCriteria) describes() bool {
	return c != nil && (c.True != nil || c.False != nil)
}

// QuestionType returns [TypeNoul].
func (q *NoulQuestion) QuestionType() string { return TypeNoul }

func (q *NoulQuestion) validate(name string) error {
	if q == nil {
		return errorf("question %q is nil", name)
	}
	if q.Instructions == nil && !q.Criteria.describes() {
		return errorf("noul question %q has neither instructions nor criteria; one of them must say what is being asked", name)
	}
	return nil
}

func (q *NoulQuestion) MarshalJSON() ([]byte, error) {
	type wire NoulQuestion
	return json.Marshal(struct {
		Type string `json:"type"`
		*wire
	}{TypeNoul, (*wire)(q)})
}

// A ChoiceQuestion selects one of a set of named alternatives. See
// [ChoiceAnswer].
type ChoiceQuestion struct {
	// Instructions is the question. A nil Instructions is sent as null.
	Instructions Entry `json:"instructions"`
	// Criteria are the alternatives to select between.
	Criteria ChoiceCriteria `json:"criteria"`
}

// ChoiceCriteria maps each label a [ChoiceQuestion] may select to a description
// of what it means. A nil description leaves the label undescribed, which is
// the right choice when the label speaks for itself:
//
//	typesafe.ChoiceCriteria{"billing": nil, "technical": nil, "other": nil}
//
// A label with no description is still an alternative; only labels present here
// can be selected. The service accepts up to 255 of them.
type ChoiceCriteria map[string]Entry

// QuestionType returns [TypeChoice].
func (q *ChoiceQuestion) QuestionType() string { return TypeChoice }

func (q *ChoiceQuestion) validate(name string) error {
	if q == nil {
		return errorf("question %q is nil", name)
	}
	if len(q.Criteria) == 0 {
		return errorf("choice question %q has no criteria; at least one label is required", name)
	}
	if len(q.Criteria) > maxChoiceCriteria {
		return errorf("choice question %q has %d criteria; at most %d labels are allowed",
			name, len(q.Criteria), maxChoiceCriteria)
	}
	return nil
}

func (q *ChoiceQuestion) MarshalJSON() ([]byte, error) {
	type wire ChoiceQuestion
	return json.Marshal(struct {
		Type string `json:"type"`
		*wire
	}{TypeChoice, (*wire)(q)})
}

// A ScoreQuestion rates the state against an ordered rubric. See [ScoreAnswer].
type ScoreQuestion struct {
	// Instructions is the question. A nil Instructions is sent as null.
	Instructions Entry `json:"instructions"`
	// Criteria is the rubric, which must describe at least two levels.
	Criteria ScoreCriteria `json:"criteria"`
}

// ScoreCriteria is a rubric: descriptions of each level in ascending order,
// scored from zero by position. The service accepts between two and ten levels,
// and any of them may be nil to leave that level undescribed.
//
//	typesafe.ScoreCriteria{"can wait", "this week", "today", "right now"}
//
// scores 0 through 3. The answer's expected score may land between levels; see
// [ScoreAnswer.Score].
type ScoreCriteria []Entry

// The sizes the service accepts, checked here so that an oversized question is
// refused where it is written rather than after a round trip. They are the
// service's numbers, not this package's: see the primitives documentation at
// https://docs.typesafe.ai/primitives, and raise them here when it does.
const (
	// minScoreCriteria: with one level there is nothing to distinguish.
	minScoreCriteria  = 2
	maxScoreCriteria  = 10
	maxChoiceCriteria = 255
)

// QuestionType returns [TypeScore].
func (q *ScoreQuestion) QuestionType() string { return TypeScore }

func (q *ScoreQuestion) validate(name string) error {
	if q == nil {
		return errorf("question %q is nil", name)
	}
	if len(q.Criteria) < minScoreCriteria || len(q.Criteria) > maxScoreCriteria {
		return errorf("score question %q has %d criteria; between %d and %d levels are required",
			name, len(q.Criteria), minScoreCriteria, maxScoreCriteria)
	}
	return nil
}

func (q *ScoreQuestion) MarshalJSON() ([]byte, error) {
	type wire ScoreQuestion
	return json.Marshal(struct {
		Type string `json:"type"`
		*wire
	}{TypeScore, (*wire)(q)})
}

// Validate reports whether the question set can be sent.
//
// [Client.SystemOne] calls it before the first attempt, so that a malformed set
// costs nothing and the error names the offending question. Call it yourself
// when questions are assembled far from where they are sent.
func (q Questions) Validate() error {
	if len(q) == 0 {
		return errorf("at least one question is required")
	}
	// Sorted so that a set with several problems always reports the same one.
	for _, name := range slices.Sorted(maps.Keys(q)) {
		if name == "" {
			// The service refuses an unnamed question, and an answer could not
			// be looked up under a name that is not there anyway.
			return errorf("a question name is empty")
		}
		question := q[name]
		if question == nil {
			return errorf("question %q is nil", name)
		}
		if err := question.validate(name); err != nil {
			return err
		}
	}
	return nil
}
