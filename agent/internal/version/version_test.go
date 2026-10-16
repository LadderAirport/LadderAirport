package version

import "testing"

func TestReportKeepsDescribeDistance(t *testing.T) {
	prev := snapshot()
	t.Cleanup(func() { restore(prev) })

	Version = "v0.15.12-3-g2ebfece"
	Commit = "2ebfece"
	ClientType = "android"
	ClientVersion = "1.1.0"
	LuciVersion = ""

	got := Report("")
	if got.AgentVersion != "v0.15.12-3-g2ebfece" || got.CoreVersion != got.AgentVersion {
		t.Fatalf("core = %+v", got)
	}
	if got.CoreCommit != "2ebfece" || got.ClientType != "android" || got.ClientVersion != "1.1.0" {
		t.Fatalf("identity = %+v", got)
	}
	if got.LuciVersion != "" {
		t.Fatalf("luci = %q", got.LuciVersion)
	}
}

func TestReportLinuxDefaults(t *testing.T) {
	prev := snapshot()
	t.Cleanup(func() { restore(prev) })

	Version = "v0.15.12"
	Commit = "unknown"
	ClientType = ""
	ClientVersion = ""
	LuciVersion = "  "

	got := Report("v0.15.12")
	if got.ClientType != "linux" || got.ClientVersion != "v0.15.12" || got.CoreCommit != "" || got.LuciVersion != "" {
		t.Fatalf("identity = %+v", got)
	}
}

func TestAllowsLinuxReleaseUpgrade(t *testing.T) {
	if !AllowsLinuxReleaseUpgrade("") || !AllowsLinuxReleaseUpgrade("linux") || !AllowsLinuxReleaseUpgrade("Linux") {
		t.Fatal("linux and empty client types stay upgradable")
	}
	if AllowsLinuxReleaseUpgrade("android") || AllowsLinuxReleaseUpgrade(" OpenWrt ") {
		t.Fatal("android and openwrt must not take a linux release binary")
	}
}

type vars struct {
	version, commit, clientType, clientVersion, luci string
}

func snapshot() vars {
	return vars{Version, Commit, ClientType, ClientVersion, LuciVersion}
}

func restore(v vars) {
	Version, Commit, ClientType, ClientVersion, LuciVersion = v.version, v.commit, v.clientType, v.clientVersion, v.luci
}
