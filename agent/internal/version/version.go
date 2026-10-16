// Package version holds build-time identity for ladder-agent.
// Values are overridden via -ldflags, for example:
//
//	-X github.com/ladderairport/agent/internal/version.Version=v0.15.12
//	-X github.com/ladderairport/agent/internal/version.Commit=2372916
//	-X github.com/ladderairport/agent/internal/version.ClientType=openwrt
//	-X github.com/ladderairport/agent/internal/version.ClientVersion=0.15.12-r2
//	-X github.com/ladderairport/agent/internal/version.LuciVersion=26.288.0734~760600c-1
//
// Version is the Ladder core version. Inject git describe --tags --always
// output: vX.Y.Z on an exact tag, or vX.Y.Z-N-gSHA when the build is ahead of
// the tag. Do not use --abbrev=0; that drops the distance and reports the
// previous tag as if it were this build.
package version

import "strings"

// Version is the Ladder core version (git describe or "0.1.0-dev").
// It is reported as both agent_version and core_version.
var Version = "0.1.0-dev"

// Commit is the short git commit of the Ladder core when injected at build time.
// Reported as core_commit. The default "unknown" is omitted from reports.
var Commit = "unknown"

// BuiltAt is a build timestamp when injected at build time.
var BuiltAt = "unknown"

// ClientType is the outer client: "linux", "android", or "openwrt".
// Stock ladder-agent builds are linux. Android and OpenWrt override this.
var ClientType = "linux"

// ClientVersion is the wrapper version (APK versionName, or OpenWrt
// PKG_VERSION-rPKG_RELEASE). Empty means the wrapper is the core binary, so
// reports use Version.
var ClientVersion = ""

// LuciVersion is the OpenWrt LuCI package version when known.
// Empty omits the field. The OpenWrt feed may set this at build time or
// overwrite it at process start after reading the installed package.
var LuciVersion = ""

// Identity is the set of version fields a client reports to the Panel.
type Identity struct {
	AgentVersion  string
	CoreVersion   string
	CoreCommit    string
	ClientType    string
	ClientVersion string
	LuciVersion   string
}

// Report builds the identity for this process.
// agentVersion is the core version the process was started with (normally
// Version). An empty agentVersion falls back to Version.
func Report(agentVersion string) Identity {
	core := strings.TrimSpace(agentVersion)
	if core == "" {
		core = strings.TrimSpace(Version)
	}
	clientVersion := strings.TrimSpace(ClientVersion)
	if clientVersion == "" {
		clientVersion = core
	}
	clientType := strings.TrimSpace(ClientType)
	if clientType == "" {
		clientType = "linux"
	}
	commit := strings.TrimSpace(Commit)
	if commit == "unknown" {
		commit = ""
	}
	return Identity{
		AgentVersion:  core,
		CoreVersion:   core,
		CoreCommit:    commit,
		ClientType:    clientType,
		ClientVersion: clientVersion,
		LuciVersion:   strings.TrimSpace(LuciVersion),
	}
}

// AllowsLinuxReleaseUpgrade reports whether Panel may push a LadderAirport
// GitHub Release binary. Android and OpenWrt ship their own wrappers.
// An empty client type is the historical linux agent and stays allowed.
func AllowsLinuxReleaseUpgrade(clientType string) bool {
	switch strings.ToLower(strings.TrimSpace(clientType)) {
	case "android", "openwrt":
		return false
	default:
		return true
	}
}
