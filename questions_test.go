package typesafe_test

import (
	"encoding/json"
	"strings"
	"testing"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

func TestQuestionMarshalling(t *testing.T) {
	tests := []struct {
		name     string
		question typesafe.Question
		want     string
	}{
		{
			"noul with no criteria",
			&typesafe.NoulQuestion{Instructions: "Is this about billing?"},
			`{"type":"noul","instructions":"Is this about billing?"}`,
		},
		{
			"noul with one side described",
			&typesafe.NoulQuestion{
				Instructions: "Is this about billing?",
				Criteria:     &typesafe.NoulCriteria{True: "yes means a charge is disputed"},
			},
			`{"type":"noul","instructions":"Is this about billing?","criteria":{"true":"yes means a charge is disputed"}}`,
		},
		{
			"noul with both sides described",
			&typesafe.NoulQuestion{
				Criteria: &typesafe.NoulCriteria{True: "a", False: "b"},
			},
			`{"type":"noul","instructions":null,"criteria":{"true":"a","false":"b"}}`,
		},
		{
			"choice",
			&typesafe.ChoiceQuestion{
				Instructions: "Which?",
				Criteria:     typesafe.ChoiceCriteria{"a": nil},
			},
			`{"type":"choice","instructions":"Which?","criteria":{"a":null}}`,
		},
		{
			"score",
			&typesafe.ScoreQuestion{
				Instructions: "How urgent?",
				Criteria:     typesafe.ScoreCriteria{"can wait", "today"},
			},
			`{"type":"score","instructions":"How urgent?","criteria":["can wait","today"]}`,
		},
		{
			// The rubric is a list and stays one: its order is its meaning.
			"score keeps rubric order",
			&typesafe.ScoreQuestion{Criteria: typesafe.ScoreCriteria{"low", nil, "high"}},
			`{"type":"score","instructions":null,"criteria":["low",null,"high"]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.question)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(encoded) != test.want {
				t.Errorf("encoded as\n%s\nwant\n%s", encoded, test.want)
			}
		})
	}
}

func TestQuestionType(t *testing.T) {
	tests := []struct {
		question typesafe.Question
		want     string
	}{
		{&typesafe.NoulQuestion{}, typesafe.TypeNoul},
		{&typesafe.ChoiceQuestion{}, typesafe.TypeChoice},
		{&typesafe.ScoreQuestion{}, typesafe.TypeScore},
	}
	for _, test := range tests {
		if got := test.question.QuestionType(); got != test.want {
			t.Errorf("%T.QuestionType() = %q, want %q", test.question, got, test.want)
		}
	}
}

func TestQuestionsValidate(t *testing.T) {
	tests := []struct {
		name      string
		questions typesafe.Questions
		want      string
	}{
		{"nil", nil, "at least one question is required"},
		{"empty", typesafe.Questions{}, "at least one question is required"},
		{"nil value", typesafe.Questions{"q": nil}, `question "q" is nil`},
		{
			// A typed nil is worth catching: it encodes as a bare null, which
			// the service would reject with a 422 far from the mistake.
			"nil typed value",
			typesafe.Questions{"q": (*typesafe.ScoreQuestion)(nil)},
			`question "q" is nil`,
		},
		{
			"short rubric",
			typesafe.Questions{"q": &typesafe.ScoreQuestion{Criteria: typesafe.ScoreCriteria{"only"}}},
			`score question "q" has 1 criteria`,
		},
		{
			// The service refuses an unnamed question, and an answer could not
			// be looked up under a name that is not there.
			"empty question name",
			typesafe.Questions{"": &typesafe.NoulQuestion{Instructions: "?"}},
			"a question name is empty",
		},
		{
			"no labels",
			typesafe.Questions{"q": &typesafe.ChoiceQuestion{}},
			`choice question "q" has no criteria`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.questions.Validate()
			if err == nil {
				t.Fatal("Validate succeeded, want an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("Validate error = %q, want it to mention %q", err, test.want)
			}
		})
	}

	valid := typesafe.Questions{
		"noul": &typesafe.NoulQuestion{Instructions: "Is this about billing?"},
		"noulByCriteria": &typesafe.NoulQuestion{
			Criteria: &typesafe.NoulCriteria{True: "the customer is unhappy"},
		},
		"choice": &typesafe.ChoiceQuestion{Criteria: typesafe.ChoiceCriteria{"a": nil}},
		"score":  &typesafe.ScoreQuestion{Criteria: typesafe.ScoreCriteria{"low", "high"}},
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate on a good set: %v", err)
	}
}

// TestQuestionsValidateIsDeterministic guards the reason Validate sorts: a set
// with two problems must always name the same one, or a failing test becomes
// a flaky one.
func TestQuestionsValidateIsDeterministic(t *testing.T) {
	questions := typesafe.Questions{
		"aaa": &typesafe.ScoreQuestion{},
		"zzz": &typesafe.ChoiceQuestion{},
	}
	first := questions.Validate()
	if first == nil {
		t.Fatal("Validate succeeded, want an error")
	}
	for range 20 {
		if got := questions.Validate(); got.Error() != first.Error() {
			t.Fatalf("Validate reported %q and then %q", first, got)
		}
	}
	if !strings.Contains(first.Error(), `"aaa"`) {
		t.Errorf("Validate reported %q, want the first name in sorted order", first)
	}
}
