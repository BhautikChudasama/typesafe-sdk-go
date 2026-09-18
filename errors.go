package typesafe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// errPrefix names this package in every error it reports, so that an error
// arriving from deep in a caller's stack says where it came from. Every error
// value and message below is built from it rather than spelling it again.
const errPrefix = "typesafe: "

// ErrNoAPIKey reports that [NewClient] found no API key in either
// [ClientOptions] or the environment.
var ErrNoAPIKey = errors.New(errPrefix + "no API key")

// errorf reports a failure that is this package's own rather than the service's:
// a setting it refuses, a request it will not send, a response it cannot read.
// None of them is worth a type of its own, but all of them owe the caller the
// package name.
func errorf(format string, args ...any) error {
	return fmt.Errorf(errPrefix+format, args...)
}

// Sentinels for the response classes the API distinguishes. Match them with
// [errors.Is] rather than by comparing [APIError.Status], which is what most
// callers actually want to ask:
//
//	if errors.Is(err, typesafe.ErrRateLimit) {
//		// back off and try later
//	}
//
// ErrServerError matches every status from 500 to 599, so an overloaded 529 is
// caught alongside a plain 500.
//
// They are constants of an unexported type rather than error variables, because
// a package-level var is writable by every importer, and one that reassigned it
// would silently change how every other importer's [errors.Is] behaved. A
// constant cannot be made to mean anything else.
const (
	ErrBadRequest statusClassError = iota + 1
	ErrAuthentication
	ErrPermissionDenied
	ErrNotFound
	ErrUnprocessableEntity
	ErrRateLimit
	ErrServerError
)

// A statusClassError is a response class that an [APIError] can be matched against.
type statusClassError uint8

func (c statusClassError) Error() string {
	return errPrefix + c.String()
}

func (c statusClassError) String() string {
	switch c {
	case ErrBadRequest:
		return "bad request"
	case ErrAuthentication:
		return "authentication failed"
	case ErrPermissionDenied:
		return "permission denied"
	case ErrNotFound:
		return "not found"
	case ErrUnprocessableEntity:
		return "unprocessable entity"
	case ErrRateLimit:
		return "rate limited"
	case ErrServerError:
		return "server error"
	default:
		return "error " + strconv.Itoa(int(c))
	}
}

// matches reports whether a response status belongs to this class.
//
// The zero value names no class, and [classOf] reports it for a status that has
// none. Letting the two meet would make every unclassified status match an
// uninitialized sentinel.
func (c statusClassError) matches(status int) bool {
	return c != 0 && classOf(status) == c
}

// classOf is the one place that maps a status code to a class, so that
// ErrServerError's range is stated once rather than again wherever it is asked
// about. It reports zero for a status with no class of its own.
func classOf(status int) statusClassError {
	switch status {
	case http.StatusBadRequest:
		return ErrBadRequest
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden:
		return ErrPermissionDenied
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnprocessableEntity:
		return ErrUnprocessableEntity
	case http.StatusTooManyRequests:
		return ErrRateLimit
	}
	if status >= 500 && status <= 599 {
		return ErrServerError
	}
	return 0
}

// An APIError is an unsuccessful HTTP response from the API, returned once the
// request's retries are exhausted or the status is not one the policy retries.
type APIError struct {
	// Status is the HTTP response status code.
	Status int
	// Header holds the HTTP response headers.
	Header http.Header
	// Body is the parsed JSON body, the response text when the body is not
	// JSON, or nil when the body is empty.
	Body any
	// RequestID is the x-typesafe-request-id header, or "" when absent. Quote
	// it in bug reports: it identifies the request in the service's own logs.
	RequestID string
}

func (e *APIError) Error() string {
	return errPrefix + strconv.Itoa(e.Status) + " " + e.describe()
}

// Is matches the class sentinels above, so that a caller can ask about the
// kind of failure without reaching for the status code.
func (e *APIError) Is(target error) bool {
	var class statusClassError
	if errors.As(target, &class) {
		return class.matches(e.Status)
	}
	return false
}

// maxRawBodyInMessage bounds how much of an unrecognized body reaches the error
// message. The full body stays available in [APIError.Body].
const maxRawBodyInMessage = 200

func (e *APIError) describe() string {
	if detail := messageFromBody(e.Body); detail != "" {
		return detail
	}
	if e.Body == nil {
		return "status code (no body)"
	}
	raw, ok := e.Body.(string)
	if !ok {
		encoded, err := json.Marshal(e.Body)
		if err != nil {
			return fmt.Sprint(e.Body)
		}
		raw = string(encoded)
	}
	// Counted and cut in runes, so that truncating a body with non-ASCII text
	// cannot leave half a character in the message.
	if runes := []rune(raw); len(runes) > maxRawBodyInMessage {
		return string(runes[:maxRawBodyInMessage]) + "…"
	}
	return raw
}

// RetryAfter reports the server's requested delay from the Retry-After or
// retry-after-ms response header, and false when neither carries a valid delay.
// It is most useful on a 429, which is the status the service documents it for.
func (e *APIError) RetryAfter() (time.Duration, bool) {
	return parseRetryAfter(e.Header, time.Now())
}

// messageFromBody extracts the human-readable part of an error body.
//
// The shapes come from the service and the frameworks in front of it, which is
// why there are several: {"error": "..."} and {"error": {"message": "..."}} are
// the service's own, while "message", "detail", and a "detail" list of
// validation errors are FastAPI's.
func messageFromBody(body any) string {
	if text, ok := body.(string); ok {
		return text
	}
	object, isObject := body.(map[string]any)
	if !isObject {
		return ""
	}
	switch field := object["error"].(type) {
	case string:
		return field
	case map[string]any:
		if message, ok := field["message"].(string); ok {
			return message
		}
	}
	if message, ok := object["message"].(string); ok {
		return message
	}
	switch detail := object["detail"].(type) {
	case string:
		return detail
	case map[string]any:
		if message, ok := detail["message"].(string); ok {
			return message
		}
	case []any:
		return describeValidationErrors(detail)
	}
	return ""
}

// describeValidationErrors formats FastAPI validation errors as
// semicolon-separated "path: message" entries, dropping the leading "body"
// element that every request-body error carries.
func describeValidationErrors(errs []any) string {
	var parts []string
	for _, entry := range errs {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		message, hasMessage := object["msg"].(string)
		if !hasMessage {
			continue
		}
		var path []string
		if loc, isList := object["loc"].([]any); isList {
			for _, segment := range loc {
				if text := fmt.Sprint(segment); text != "body" {
					path = append(path, text)
				}
			}
		}
		if len(path) > 0 {
			message = strings.Join(path, ".") + ": " + message
		}
		parts = append(parts, message)
	}
	return strings.Join(parts, "; ")
}

// A ConnectionError reports that an attempt produced no complete HTTP response:
// the connection failed, the server closed it mid-body, or the attempt ran out
// of time.
//
// A timeout is the same failure as a dropped connection from the caller's point
// of view — no answer arrived — so it is this type with [ConnectionError.Timeout]
// set, not a type of its own. Ask [errors.Is] for [context.DeadlineExceeded] to
// tell the two apart without inspecting the field.
type ConnectionError struct {
	// Timeout is the per-attempt timeout that elapsed, and zero when the
	// attempt failed for another reason.
	Timeout time.Duration
	// Err is the underlying transport or context error.
	Err error
}

func (e *ConnectionError) Error() string {
	if e.Timeout > 0 {
		return errPrefix + "request timed out after " + e.Timeout.String() + ": " + e.Err.Error()
	}
	return errPrefix + "connection error: " + e.Err.Error()
}

func (e *ConnectionError) Unwrap() error { return e.Err }
