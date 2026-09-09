// Package version holds build-time version metadata for hlauncher.
package version

// These can be overridden at build time with:
//   -ldflags "-X github.com/hlauncher/hlauncher/internal/version.Version=1.2.3"
var (
	// Version is the semantic version of this build.
	Version = "0.1.0"
	// BuildMode is "development" or "production".
	BuildMode = "development"
	// AppName is the product name used for paths, mutex names, window titles.
	AppName = "hlauncher"
)

// IsProduction reports whether this is a production build.
func IsProduction() bool { return BuildMode == "production" }
