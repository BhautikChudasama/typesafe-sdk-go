package typesafe_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	typesafe "github.com/BhautikChudasama/typesafe-sdk-go"
)

const answersJSON = `{
  "yes":  {"type": "noul", "noul": 0.82},
  "tone": {"type": "choice", "choice": "frustrated", "confidence": 0.71,
           "probabilities": {"calm": 0.1, "frustrated": 0.7, "angry": 0.2}},
  "rush": {"type": "score", "score": 2.4, "confidence": 0.63,
           "legend": {"0": "can wait", "1": "this week", "2": "today", "3": null},
           "probabilities": {"0": 0.05, "1": 0.15, "2": 0.4, "3": 0.4}}
}`

func decodeAnswers(t *testing.T, raw string) typesafe.Answers {
	t.Helper()
	var answers typesafe.Answers
	if err := json.Unmarshal([]byte(raw), &answers); err != nil {
		t.Fatalf("decoding answers: %v", err)
	}
	return answers
}

func TestAnswersDecode(t *testing.T) {
	answers := decodeAnswers(t, answersJSON)

	noul, err := answers.Noul("yes")
	if err != nil {
		t.Fatalf("Noul: %v", err)
	}
	if noul.Noul != 0.82 {
		t.Errorf("Noul = %v, want 0.82", noul.Noul)
	}

	choice, err := answers.Choice("tone")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if choice.Choice != "frustrated" || choice.Confidence != 0.71 {
		t.Errorf("Choice = %+v", choice)
	}
	wantProbabilities := map[string]float64{"calm": 0.1, "frustrated": 0.7, "angry": 0.2}
	if !reflect.DeepEqual(choice.Probabilities, wantProbabilities) {
		t.Errorf("Probabilities = %v, want %v", choice.Probabilities, wantProbabilities)
	}

	score, err := answers.Score("rush")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Score != 2.4 || score.Confidence != 0.63 {
		t.Errorf("Score = %+v", score)
	}
	// The wire keys a rubric by a numeric string; a Go caller indexes it by the
	// score itself.
	if got := score.Legend[2]; got != "today" {
		t.Errorf("Legend[2] = %v, want %q", got, "today")
	}
	if got, ok := score.Legend[3]; !ok || got != nil {
		t.Errorf("Legend[3] = %v (present %v), want an undescribed level", got, ok)
	}
	if got := score.Probabilities[2]; got != 0.4 {
		t.Errorf("Probabilities[2] = %v, want 0.4", got)
	}
}

func TestChoiceAnswerProbability(t *testing.T) {
	answers := decodeAnswers(t, answersJSON)
	choice, err := answers.Choice("tone")
	if err != nil {
		t.Fatalf("Choice: %v", err)
	}
	if got := choice.Probability(); got != 0.7 {
		t.Errorf("Probability() = %v, want the selected label's 0.7", got)
	}
	// Reported confidence is a different number, and conflating the two is the
	// mistake the method exists to make harder.
	if choice.Probability() == choice.Confidence {
		t.Error("the test fixture no longer distinguishes probability from confidence")
	}

	unreported := &typesafe.ChoiceAnswer{Choice: "missing", Probabilities: map[string]float64{"a": 1}}
	if got := unreported.Probability(); got != 0 {
		t.Errorf("Probability() = %v for an unreported label, want 0", got)
	}
}

func TestScoreAnswerLevelAndDescription(t *testing.T) {
	legend := map[int]typesafe.Entry{0: "can wait", 1: "this week", 2: "today", 3: nil}
	tests := []struct {
		score       float64
		wantLevel   int
		wantDescrip typesafe.Entry
	}{
		{0, 0, "can wait"},
		{1.4, 1, "this week"},
		{1.5, 2, "today"},
		{2.4, 2, "today"},
		{2.6, 3, nil},
	}
	for _, test := range tests {
		answer := &typesafe.ScoreAnswer{Score: test.score, Legend: legend}
		if got := answer.Level(); got != test.wantLevel {
			t.Errorf("Level() for %v = %d, want %d", test.score, got, test.wantLevel)
		}
		if got := answer.Description(); got != test.wantDescrip {
			t.Errorf("Description() for %v = %v, want %v", test.score, got, test.wantDescrip)
		}
	}

	// A level the rubric never described reads the same as one it described as
	// null: undescribed.
	noLegend := &typesafe.ScoreAnswer{Score: 9}
	if got := noLegend.Description(); got != nil {
		t.Errorf("Description() off the rubric = %v, want nil", got)
	}
}

func TestAnswersAccessorErrors(t *testing.T) {
	answers := decodeAnswers(t, answersJSON)

	if _, err := answers.Noul("missing"); err == nil {
		t.Error("Noul on an absent name succeeded")
	} else if !strings.Contains(err.Error(), "not in the response") {
		t.Errorf("error = %q", err)
	}

	_, err := answers.Score("tone")
	if err == nil {
		t.Fatal("Score on a choice answer succeeded")
	}
	if !strings.Contains(err.Error(), "want a score answer, got choice") {
		t.Errorf("error = %q", err)
	}
}

func TestAnswersKeepUnknownTypes(t *testing.T) {
	answers := decodeAnswers(t, `{
	  "known":   {"type": "noul", "noul": 0.5},
	  "novel":   {"type": "quantum", "spin": "up"}
	}`)

	// One unrecognized answer must not cost the caller the answers beside it.
	if _, err := answers.Noul("known"); err != nil {
		t.Errorf("Noul on the known answer: %v", err)
	}

	unknown, ok := answers["novel"].(*typesafe.UnknownAnswer)
	if !ok {
		t.Fatalf("novel decoded as %T, want an *UnknownAnswer", answers["novel"])
	}
	if unknown.AnswerType() != "quantum" {
		t.Errorf("AnswerType() = %q", unknown.AnswerType())
	}
	if !jsonEqual(decode(t, unknown.Raw), map[string]any{"type": "quantum", "spin": "up"}) {
		t.Errorf("Raw = %s", unknown.Raw)
	}

	// The accessors still refuse it: an answer the SDK cannot interpret is
	// never passed off as one it can.
	if _, err := answers.Noul("novel"); err == nil {
		t.Error("Noul accepted an unknown answer type")
	}
}

func TestAnswersRejectMalformedPayloads(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"not an object", `[]`},
		// A null decodes into a struct without complaint, so it has to be
		// refused on its own rather than becoming a typeless UnknownAnswer.
		{"null answer", `{"q": null}`},
		{"answer is not an object", `{"q": 3}`},
		{"wrong field type", `{"q": {"type": "noul", "noul": "high"}}`},
		{"legend key is not a score", `{"q": {"type": "score", "legend": {"low": "x"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var answers typesafe.Answers
			if err := json.Unmarshal([]byte(test.raw), &answers); err == nil {
				t.Fatalf("decoding %s succeeded, want an error", test.raw)
			}
		})
	}
}

func TestAnswersRoundTrip(t *testing.T) {
	answers := decodeAnswers(t, answersJSON)
	encoded, err := json.Marshal(answers)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !jsonEqual(decode(t, encoded), decode(t, []byte(answersJSON))) {
		t.Errorf("round trip produced\n%s\nwant\n%s", encoded, answersJSON)
	}

	// An answer the SDK does not model survives the round trip byte for byte,
	// which is the only honest thing to do with a payload it cannot interpret.
	unknown := decodeAnswers(t, `{"novel": {"type": "quantum", "spin": "up"}}`)
	encoded, err = json.Marshal(unknown)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !jsonEqual(decode(t, encoded), map[string]any{
		"novel": map[string]any{"type": "quantum", "spin": "up"},
	}) {
		t.Errorf("round trip produced %s", encoded)
	}
}

func TestUnknownAnswerWithoutRawRefusesToEncode(t *testing.T) {
	_, err := json.Marshal(typesafe.Answers{"q": &typesafe.UnknownAnswer{Type: "quantum"}})
	if err == nil {
		t.Fatal("encoding an UnknownAnswer with no raw JSON succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "no raw JSON") {
		t.Errorf("error = %q", err)
	}
}

func TestAnswerType(t *testing.T) {
	tests := []struct {
		answer typesafe.Answer
		want   string
	}{
		{&typesafe.NoulAnswer{}, typesafe.TypeNoul},
		{&typesafe.ChoiceAnswer{}, typesafe.TypeChoice},
		{&typesafe.ScoreAnswer{}, typesafe.TypeScore},
		{&typesafe.UnknownAnswer{Type: "quantum"}, "quantum"},
	}
	for _, test := range tests {
		if got := test.answer.AnswerType(); got != test.want {
			t.Errorf("%T.AnswerType() = %q, want %q", test.answer, got, test.want)
		}
	}
}
