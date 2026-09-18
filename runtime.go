package typesafe

import (
	"runtime"
	"strings"
)

// runtimeDescription is computed once: the toolchain and platform a process
// runs on cannot change while it runs, and every client would otherwise hold
// its own copy of the same answer.
var runtimeDescription = describeRuntime()

// describeRuntime reports the Go toolchain and platform for the
// X-TypeSafe-Runtime header, in the shape the other SDKs use for their own
// runtimes: name/version (os; arch).
//
// runtime.Version reports a toolchain name such as "go1.25.0" for a release and
// "devel +hash" for an unreleased tree, so the prefix is trimmed but whatever
// remains is reported as it stands rather than being parsed.
func describeRuntime() string {
	return "go/" + strings.TrimPrefix(runtime.Version(), "go") +
		" (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
}
