// Package version owns the single authoritative application version.
//
// Every user-visible version string derives from Version: the CLI, the
// dashboard, and release or package metadata. A release advances the version
// by changing this one literal. Schema and protocol compatibility versions are
// separate concepts and must not be confused with it.
package version

// Version is the only application version literal in the source tree.
const Version = "1.0.3"
