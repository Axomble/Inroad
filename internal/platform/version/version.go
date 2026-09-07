// Package version carries the build's identity: the release tag, the commit it
// was built from, and when. The values are stamped at link time by the release
// pipeline (-ldflags "-X .../version.Version=v1.2.3 ..."); an un-stamped build
// (go run, go build, a developer's binary) reports "dev".
//
// It is deliberately data-only — no init, no I/O — so any package may import it
// without pulling in a dependency or a side effect.
package version

// Stamped at link time. Keep the var names stable: the ldflags paths in
// deploy/docker/Dockerfile.* and .github/workflows/release.yml reference them by
// their full import path.
var (
	// Version is the release tag (e.g. "v1.4.0") or "dev" for an un-stamped build.
	Version = "dev"
	// Commit is the short git SHA the build came from, or "unknown".
	Commit = "unknown"
	// Date is the build timestamp in RFC 3339, or "unknown".
	Date = "unknown"
)

// String is "<version> (<commit>, <date>)" — one field for a startup log line.
func String() string {
	return Version + " (" + Commit + ", " + Date + ")"
}
