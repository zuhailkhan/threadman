package cli

import "runtime/debug"

// version is set at release build time:
//
//	go build -ldflags "-X github.com/zuhailkhan/threadman/internal/cli.version=v0.1.0"
var version string

// Version reports the release version. Binaries built without the flag fall
// back to the module version Go records for `go install ...@vX.Y.Z`, and to
// "dev" for a local build.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
