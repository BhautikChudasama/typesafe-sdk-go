package typesafe

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// An Answer is one of [*NoulAnswer], [*ChoiceAnswer], [*ScoreAnswer], or
// [*UnknownAnswer].
//
// A question's type decides its answer's, but Go cannot give a map values of
// different types per key, so the type arrives at run time instead: use
// [Answers.Noul], [Answers.Choice], and [Answers.Score], which name the type
// you expect and report a clear error when the service disagrees.
type Answer interface {
	// AnswerType reports the wire discriminant: [TypeNoul], [TypeChoice],
	// [TypeScore], or whatever unrecognized type the service sent.
	AnswerType() string

	answer()
}

// Answers are the answers to one request, keyed by the names the request's
// [Questions] used.
type Answers map[string]Answer

// A NoulAnswer answers a [NoulQuestion].
//
// It deliberately offers no Yes or Above helper. A threshold is the one part of
// a yes/no decision this package does not know: it depends on what a wrong yes
// and a wrong no each cost you. A helper would make `answer.Yes()` the obvious
// thing to write and bury that choice at whatever default it shipped with.
type NoulAnswer struct {
	// Noul is the probability that the answer is yes, from zero to one.
	//
	// It is a probability and not a verdict, which is the point of the
	// primitive: pick the threshold your application needs instead of
	// accepting one the service picked for you.
	Noul float64 `json:"noul"`
}

// AnswerType returns [TypeNoul].
func (a *NoulAnswer) AnswerType() string { return TypeNoul }
func (a *NoulAnswer) answer()            {}

func (a *NoulAnswer) MarshalJSON() ([]byte, error) {
	type wire NoulAnswer
	return json.Marshal(struct {
		Type string `json:"type"`
		*wire
	}{TypeNoul, (*wire)(a)})
}

// A ChoiceAnswer answers a [ChoiceQuestion].
type ChoiceAnswer struct {
	// Choice is the selected label, one of the keys of the question's
	// [ChoiceCriteria].
	Choice string `json:"choice"`
	// Confidence is the service's reported confidence in Choice, from zero to
	// one. It is not the same as the selected label's probability: see
	// Probabilities.
	Confidence float64 `json:"confidence"`
	// Probabilities holds one probability per label of the question.
	Probabilities map[string]float64 `json:"probabilities"`
}

// AnswerType returns [TypeChoice].
func (a *ChoiceAnswer) AnswerType() string { return TypeChoice }
func (a *ChoiceAnswer) answer()            {}

// Probability reports how likely the selected label was, which is the reading
// of a choice that callers reach for and the one that is easy to confuse with
// Confidence. It is zero if the service selected a label it reported no
// probability for, which a well-formed answer never does.
func (a *ChoiceAnswer) Probability() float64 {
	return a.Probabilities[a.Choice]
}

func (a *ChoiceAnswer) MarshalJSON() ([]byte, error) {
	type wire ChoiceAnswer
	return json.Marshal(struct {
		Type string `json:"type"`
		*wire
	}{TypeChoice, (*wire)(a)})
}

// A ScoreAnswer answers a [ScoreQuestion].
type ScoreAnswer struct {
	// Score is the expected score over the rubric, so it may fall between two
	// levels: a question split evenly between levels 1 and 3 scores 2, which no
	// single level was chosen for.
	Score float64 `json:"score"`
	// Confidence is the service's reported confidence in Score, from zero to
	// one.
	Confidence float64 `json:"confidence"`
	// Legend is the question's rubric echoed back, keyed by score, so that a
	// score can be rendered without holding on to the request.
	Legend map[int]Entry `json:"legend"`
	// Probabilities holds one probability per level of the rubric.
	Probabilities map[int]float64 `json:"probabilities"`
}

// AnswerType returns [TypeScore].
func (a *ScoreAnswer) AnswerType() string { return TypeScore }
func (a *ScoreAnswer) answer()            {}

// Level reports the rubric level the expected score lands on, rounding a score
// that fell between two of them.
//
// Round to render a score, not to decide on one: rounding 1.5 and 2.4 to the
// same level discards the difference the expected score exists to express.
func (a *ScoreAnswer) Level() int {
	return int(math.Round(a.Score))
}

// Description reports what the rubric says about [ScoreAnswer.Level], and nil
// when that level was left undescribed.
func (a *ScoreAnswer) Description() Entry {
	return a.Legend[a.Level()]
}

func (a *ScoreAnswer) MarshalJSON() ([]byte, error) {
	type wire ScoreAnswer
	return json.Marshal(struct {
		Type string `json:"type"`
		*wire
	}{TypeScore, (*wire)(a)})
}

// An UnknownAnswer carries an answer whose type this SDK does not model, which
// is what a service newer than the SDK produces.
//
// Decoding keeps it rather than failing, so that one unrecognized answer does
// not cost you the answers beside it in the same response. The accessors still
// refuse it: an answer the SDK cannot interpret is never silently treated as
// one it can.
type UnknownAnswer struct {
	// Type is the discriminant the service sent.
	Type string
	// Raw is the answer's undecoded JSON.
	Raw json.RawMessage
}

// AnswerType returns the unrecognized discriminant the service sent.
func (a *UnknownAnswer) AnswerType() string { return a.Type }
func (a *UnknownAnswer) answer()            {}

// MarshalJSON returns the bytes the service sent. An answer this package could
// not interpret is one it has no business rewriting.
func (a *UnknownAnswer) MarshalJSON() ([]byte, error) {
	if len(a.Raw) == 0 {
		return nil, errorf("an UnknownAnswer of type %q carries no raw JSON to encode", a.Type)
	}
	return a.Raw, nil
}

// UnmarshalJSON decodes each answer into the type its discriminant names.
func (a *Answers) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return errorf("decoding answers: %w", err)
	}
	answers := make(Answers, len(raw))
	for name, message := range raw {
		answer, err := unmarshalAnswer(message)
		if err != nil {
			return errorf("answer %q: %w", name, err)
		}
		answers[name] = answer
	}
	*a = answers
	return nil
}

func unmarshalAnswer(message json.RawMessage) (Answer, error) {
	// A null is not an answer of an unknown type, it is the absence of one, and
	// it decodes into a struct without complaint. Every other non-object fails
	// on the read below; this one has to be caught here.
	if string(message) == "null" {
		return nil, errors.New("the answer is null")
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(message, &head); err != nil {
		return nil, fmt.Errorf("reading the answer type: %w", err)
	}
	var answer Answer
	switch head.Type {
	case TypeNoul:
		answer = new(NoulAnswer)
	case TypeChoice:
		answer = new(ChoiceAnswer)
	case TypeScore:
		answer = new(ScoreAnswer)
	default:
		return &UnknownAnswer{Type: head.Type, Raw: message}, nil
	}
	if err := json.Unmarshal(message, answer); err != nil {
		return nil, fmt.Errorf("decoding a %s answer: %w", head.Type, err)
	}
	return answer, nil
}

// Noul returns the answer to the named noul question. It reports an error when
// the response holds no answer under that name, or answered it with a different
// type than the question asked for.
func (a Answers) Noul(name string) (*NoulAnswer, error) {
	return answerOf[*NoulAnswer](a, name, TypeNoul)
}

// Choice returns the answer to the named choice question, on the same terms as
// [Answers.Noul].
func (a Answers) Choice(name string) (*ChoiceAnswer, error) {
	return answerOf[*ChoiceAnswer](a, name, TypeChoice)
}

// Score returns the answer to the named score question, on the same terms as
// [Answers.Noul].
func (a Answers) Score(name string) (*ScoreAnswer, error) {
	return answerOf[*ScoreAnswer](a, name, TypeScore)
}

func answerOf[T Answer](answers Answers, name, want string) (T, error) {
	var zero T
	answer, ok := answers[name]
	if !ok {
		return zero, errorf("answer %q: not in the response", name)
	}
	typed, ok := answer.(T)
	if !ok {
		return zero, errorf("answer %q: want a %s answer, got %s", name, want, answer.AnswerType())
	}
	return typed, nil
}
