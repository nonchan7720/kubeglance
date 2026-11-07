// Package version holds the build version. release-please bumps the literal
// (x-release-please-version); goreleaser overrides it with the tag via
// -ldflags "-X github.com/nonchan7720/kubeglance/pkg/version.Version=...".
package version

// Version is the kubeglance version.
var Version = "0.1.0" // x-release-please-version
