# Changelog

## v0.0.1 (2026-09-18)

First release of the Go SDK for the TypeSafe AI API.

- `Client.SystemOne` for noul, choice, and score questions, with `Answers.Noul`,
  `Answers.Choice`, and `Answers.Score` naming the answer type at the point of
  use.
- `Client.ListModels`.
- Configuration from `ClientOptions`, then `TYPESAFE_API_KEY`,
  `TYPESAFE_BASE_URL`, `TYPESAFE_DEFAULT_MODEL`, and `TYPESAFE_LOG_LEVEL`, then
  the SDK defaults.
- Retries of 408, 429, and 5xx responses along with connection failures and
  timeouts, with exponential backoff, jitter, and `Retry-After` support.
- `*APIError` with class sentinels for `errors.Is`, `*ConnectionError` for
  requests that never completed, and the caller's own context error for
  cancellation.
- `log/slog` request and response logging with credential headers redacted.
- Request validation for the rules the service enforces, verified against the
  live API: a required `State`, a noul that carries a question in either
  `Instructions` or `Criteria`, a non-empty question name, 1 to 255 choice
  labels, and 2 to 10 score levels. Each is refused before a request is sent
  rather than after a 400.
- Integration tests (`-tags integration`) that check those limits from both
  sides, so a change to the service's rules fails a test instead of surprising
  a caller. Unit tests run against response payloads captured from the live
  service under `testdata/`.

See [Design notes](README.md#design-notes) for the choices behind the API shape.
