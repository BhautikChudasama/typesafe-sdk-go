# Repository instructions

Engineering conventions that apply to every change live in [`AGENTS.md`](AGENTS.md). The rules below hold only
for this repository, and each one resolves a question `AGENTS.md` deliberately leaves open.

- **The service owns the wire grammar.** Where this SDK disagrees with the TypeSafe API, the API wins.
  Implement what the service actually accepts and returns; do not invent a local dialect, and do not model a
  field the service does not send.

- **Behavior is settled against the running service, not against prose.** The documentation says what the API
  is for; only the API says what it accepts. A rule this package enforces locally — a size, a required field,
  a refused shape — earns its place by having been observed, and carries an integration test that checks it
  from both sides so that a service which relaxes the rule fails a test instead of leaving this package
  stricter than the API.

- **A design decision that is not obvious is written down.** Where this package answers something differently
  from how a reader might expect, the reason belongs in the doc comment of the type that carries it, or in
  `README.md` where it shapes how the whole SDK is used.

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
