package typesafe

// Version is the version of this SDK, reported in the User-Agent and
// X-TypeSafe-SDK request headers. It versions this module, not the API it
// speaks to.
const Version = "0.0.1"

// userAgent names this SDK to the service, which keeps a Go client separable
// from the other SDKs' traffic in their logs.
const userAgent = "typesafe-sdk-go/" + Version
