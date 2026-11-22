// Package version holds the semantic version stamped into every Sumi binary.
package version

// Version is the release the binary was built from. Builds set it with
//
//	-ldflags "-X github.com/abcdlsj/sumi/internal/version.Version=6.0.4"
//
// and unstamped development builds keep the marker below.
var Version = Dev

// Dev marks a binary that was not built for a release.
const Dev = "0.0.0-dev"

// Value returns the stamped version, falling back to the development marker.
func Value() string {
	if Version == "" {
		return Dev
	}
	return Version
}
