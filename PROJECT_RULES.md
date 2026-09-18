# Repository instructions

Engineering conventions that apply to every change live in [`AGENTS.md`](AGENTS.md). The rules below hold only
for this repository, and each one resolves a question `AGENTS.md` deliberately leaves open.

- **The service owns the wire grammar.** Where this SDK disagrees with the TypeSafe API, the API wins. Port
  what the service actually accepts and returns; do not invent a local dialect, and do not model a field the
  service does not send.

- **The JavaScript SDK is the reference, not the template.** `typesafe-sdk-js` defines the behavior to match:
  the same headers, the same retry arithmetic, the same error messages extracted from the same body shapes.
  Its *shape* is not binding. Where Go has a better answer — a context instead of an AbortSignal, a typed
  error instead of a class hierarchy, a slice type instead of a runtime check — take it, and say in a comment
  or the README what the port does differently and why.

- **Deviations from the reference need a stated reason.** A behavioral difference that is neither documented
  nor tested is a bug. Every intentional one is named in `README.md` or in the doc comment of the type that
  carries it.

- **Prefer the standard library.** The SDK has no third-party dependencies and should keep none: `net/http`,
  `encoding/json`, `log/slog`, and `context` cover everything it does. A dependency needs a reason the
  standard library cannot meet, not merely a convenience.

- **Use explicit `Config`- and `Options`-style structs** for related settings, and give optional fields useful
  zero meanings. Do not introduce functional-options APIs.

- **A setting struct is a complete value, not a patch.** `RetryPolicy` is the example: a nil pointer inherits
  the level above it and a non-nil one replaces it outright, so no field needs a third state to distinguish
  "unset" from its zero. Provide a `DefaultX()` constructor for anything a caller is expected to start from.

- **Backward compatibility is not a goal before v1.** Remove obsolete paths instead of adding compatibility
  layers. Make architectural decisions for the long term while breaking changes are still inexpensive.

- **Respond to an unrecognized payload; do not fail the whole response.** A newer service may send an answer
  type or a field this release does not model. Keep it (`UnknownAnswer`, `Meta.Body`) rather than discarding
  the answers beside it, and keep the typed accessors strict so nothing unrecognized is passed off as
  understood.

- **Never log a credential.** Anything added to the request path that touches headers must go through the
  redaction in `logging.go`, and the test that proves a secret cannot reach a handler must keep passing.

- **The lint gate is `golangci-lint run ./...`, and it is clean.** Fix the finding rather than silencing it.
  Where a rule genuinely does not apply, exclude it in `.golangci.yml` with the reason written out, scoped as
  narrowly as the rule allows; an exclusion that stops excluding anything is deleted.
