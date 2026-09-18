package typesafe

import (
	"context"
	"net/http"
)

// A ModelCard describes one model available to the account.
type ModelCard struct {
	// Name is the identifier to put in [SystemOneRequest.Model].
	Name string `json:"name"`
	// Description is a human-readable summary of what the model is for.
	Description string `json:"description"`
	// ReleaseDate is the model's release date, as the service formats it.
	ReleaseDate string `json:"release_date"`
}

// A ModelList holds the models available to the account.
type ModelList struct {
	// Models are the available models. An account with none is answered with
	// an empty list rather than an error.
	Models []ModelCard `json:"models"`
	// Meta reports HTTP metadata about the response. Its Body holds any model
	// attribute [ModelCard] does not name, such as one added after this
	// release or one visible only to the service's own accounts.
	Meta Meta `json:"-"`
}

// Names reports the model names, which is what a caller needs to offer a
// choice or to check that the model they mean to use is one of them.
func (l *ModelList) Names() []string {
	names := make([]string, len(l.Models))
	for i, model := range l.Models {
		names[i] = model.Name
	}
	return names
}

// ListModels reports the models available to the account.
func (c *Client) ListModels(ctx context.Context, options *RequestOptions) (*ModelList, error) {
	// A body absent from the wire is different from a body that decoded to an
	// empty list, and only the second is a model list. Decoding into a pointer
	// tells them apart.
	var wire *ModelList
	meta, err := c.do(ctx, http.MethodGet, "/v1/models", nil, options, &wire)
	if err != nil {
		return nil, err
	}
	if wire == nil || wire.Models == nil {
		return nil, errorf("GET /v1/models: %s is not a model list, want {\"models\": [...]}: %s", meta.describe(), meta.Body)
	}
	wire.Meta = meta
	return wire, nil
}
