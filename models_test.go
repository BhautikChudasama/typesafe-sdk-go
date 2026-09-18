package typesafe_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

func TestListModels(t *testing.T) {
	cards := []any{
		map[string]any{"name": "jev-1.13", "description": "the current model", "release_date": "2026-07-01"},
	}
	client, rec := newTestClient(t, always(http.StatusOK, map[string]any{"models": cards}), nil)

	list, err := client.ListModels(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if rec.Last(t).URL != "https://api.test/v1/models" {
		t.Errorf("url = %q", rec.Last(t).URL)
	}
	want := []typesafe.ModelCard{
		{Name: "jev-1.13", Description: "the current model", ReleaseDate: "2026-07-01"},
	}
	if !reflect.DeepEqual(list.Models, want) {
		t.Errorf("Models = %+v, want %+v", list.Models, want)
	}
	if list.Meta.Status != http.StatusOK {
		t.Errorf("Meta.Status = %d", list.Meta.Status)
	}
}

func TestModelListNames(t *testing.T) {
	list := &typesafe.ModelList{Models: []typesafe.ModelCard{{Name: "a"}, {Name: "b"}}}
	if got := list.Names(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Names() = %v", got)
	}
	if got := (&typesafe.ModelList{}).Names(); len(got) != 0 {
		t.Errorf("Names() of an empty list = %v, want none", got)
	}
}

func TestListModelsEmptyIsNotAnError(t *testing.T) {
	client, _ := newTestClient(t, always(http.StatusOK, map[string]any{"models": []any{}}), nil)

	list, err := client.ListModels(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(list.Models) != 0 {
		t.Errorf("Models = %+v, want none", list.Models)
	}
}

func TestListModelsKeepsUnmodelledFields(t *testing.T) {
	wire := map[string]any{"models": []any{
		map[string]any{
			"name": "jev-1.13", "description": "d", "release_date": "2026",
			"tags": []any{"internal"},
		},
	}}
	client, _ := newTestClient(t, always(http.StatusOK, wire), nil)

	list, err := client.ListModels(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// A field ModelCard does not name is dropped from the typed value, and
	// Meta.Body is where it is still reachable.
	if !jsonEqual(decode(t, list.Meta.Body), wire) {
		t.Errorf("Meta.Body = %s, want the whole response", list.Meta.Body)
	}
}

// TestListModelsErrorNamesTheRequest keeps the identifier a caller would have
// to quote in the one error where the SDK, not the service, is the one
// complaining about the payload.
func TestListModelsErrorNamesTheRequest(t *testing.T) {
	header := http.Header{"X-Typesafe-Request-Id": {"req_shape"}}
	client, _ := newTestClient(t, func(int, recordedRequest) (*http.Response, error) {
		return jsonResponse(http.StatusOK, map[string]any{"ok": true}, header), nil
	}, nil)

	_, err := client.ListModels(t.Context(), nil)
	if err == nil {
		t.Fatal("ListModels accepted an unrecognized shape")
	}
	if !strings.Contains(err.Error(), "req_shape") {
		t.Errorf("error = %q, want it to quote the request id", err)
	}
}

func TestListModelsRejectsUnrecognizedShapes(t *testing.T) {
	shapes := []any{
		nil,
		[]any{},
		map[string]any{"models": map[string]any{"models": []any{}}},
		map[string]any{"models": nil},
		map[string]any{"models": "bad"},
		map[string]any{"ok": true},
	}
	for _, shape := range shapes {
		t.Run(string(mustJSON(shape)), func(t *testing.T) {
			client, _ := newTestClient(t, always(http.StatusOK, shape), nil)
			if _, err := client.ListModels(t.Context(), nil); err == nil {
				t.Fatalf("ListModels accepted %s", mustJSON(shape))
			}
		})
	}
}
