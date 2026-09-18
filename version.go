package typesafe

// Version is the version of this SDK, reported in the User-Agent and
// X-TypeSafe-SDK request headers. It is this module's own version; the wire
// contract it implements is the JavaScript SDK's v0.6.0.
const Version = "0.0.1"

// userAgent names this SDK to the service, which keeps a Go client separable
// from the other SDKs' traffic in their logs.
const userAgent = "typesafe-sdk-go/" + Version
