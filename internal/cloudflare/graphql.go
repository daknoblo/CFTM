package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// graphQLPath is outside the account-scoped tree: the query itself names the
// account or zone it asks about.
const graphQLPath = "/graphql"

// graphQLRequest is the wire format. Values always travel in variables, never
// interpolated into the query, so a hostname can never alter its shape.
type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLEnvelope[T any] struct {
	Data   T              `json:"data"`
	Errors []graphQLError `json:"errors"`
}

// graphQLError is one entry of the errors array.
type graphQLError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// GraphQLError reports a query the Analytics API refused. Cloudflare answers
// these with HTTP 200 and a populated errors array, so they never arrive as an
// *APIError and the status code says nothing about them.
type GraphQLError struct {
	Errors []string
	// Authz marks a permission problem, taken from the documented
	// extensions.code rather than guessed from the message text.
	Authz bool
}

func (e *GraphQLError) Error() string {
	return "cloudflare graphql: " + strings.Join(e.Errors, "; ")
}

// IsAuth reports a missing permission, matching the *APIError behavior the
// capability recorder relies on.
func (e *GraphQLError) IsAuth() bool { return e.Authz }

// graphQL runs one query and decodes the data block into T.
func graphQL[T any](ctx context.Context, c *Client, query string, variables map[string]any) (T, error) {
	var zero T

	payload, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return zero, fmt.Errorf("cloudflare: encode graphql query: %w", err)
	}

	body, err := c.send(ctx, request{
		method:  http.MethodPost,
		path:    graphQLPath,
		url:     c.baseURL + graphQLPath,
		body:    payload,
		graphQL: true,
	})
	if err != nil {
		return zero, err
	}

	var env graphQLEnvelope[T]
	if err := json.Unmarshal(body, &env); err != nil {
		return zero, fmt.Errorf("cloudflare: decode graphql response: %w", err)
	}
	if len(env.Errors) > 0 {
		return zero, newGraphQLError(env.Errors)
	}
	return env.Data, nil
}

func newGraphQLError(errs []graphQLError) *GraphQLError {
	out := &GraphQLError{Errors: make([]string, 0, len(errs))}
	for _, e := range errs {
		out.Errors = append(out.Errors, e.Message)
		if e.Extensions.Code == "authz" {
			out.Authz = true
		}
	}
	return out
}
