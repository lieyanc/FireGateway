// Package version holds build metadata injected at link time via
// -ldflags "-X github.com/lieyanc/FireGateway/internal/version.Version=...".
package version

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Info returns the build metadata as a JSON-friendly map.
func Info() map[string]string {
	return map[string]string{
		"version":   Version,
		"commit":    Commit,
		"buildTime": BuildTime,
	}
}
