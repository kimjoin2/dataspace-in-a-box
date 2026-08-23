package main

import (
	"runtime/debug"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{
			name: "no build info at all",
			ok:   false,
			want: "unknown",
		},
		{
			// go install ...@v0.3.1: a real tag, and no VCS stamp because it
			// was not built from a working tree.
			name: "a build installed from a module version reports its tag",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.3.1"}},
			ok:   true,
			want: "v0.3.1",
		},
		{
			// What every build of this repository currently looks like: the
			// toolchain stamps a pseudo-version alongside the VCS settings.
			// The revision wins, because the pseudo-version's own copy of it
			// is twelve characters and does not match git log --oneline.
			name: "a working-tree build prefers the revision over the pseudo-version",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "v0.0.0-20260823144454-4733c5821a23+dirty"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "4733c5821a23e4a0f2d6c8b1179ee3a5c0d2f8e9"},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			ok:   true,
			want: "4733c58-dirty",
		},
		{
			name: "an untagged build reports the short revision",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "4733c58ab19e4a0f2d6c8b1179ee3a5c0d2f8e91"},
				},
			},
			ok:   true,
			want: "4733c58",
		},
		{
			name: "an untagged build from a dirty tree says so",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "4733c58ab19e4a0f2d6c8b1179ee3a5c0d2f8e91"},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			ok:   true,
			want: "4733c58-dirty",
		},
		{
			name: "a clean tree is not marked dirty",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "4733c58ab19e4a0f2d6c8b1179ee3a5c0d2f8e91"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			ok:   true,
			want: "4733c58",
		},
		{
			name: "an untagged build with no VCS stamp has nothing to report",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			ok:   true,
			want: "unknown",
		},
		{
			name: "a revision shorter than the truncation point is left alone",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc12"},
				},
			},
			ok:   true,
			want: "abc12",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildVersion(tt.info, tt.ok); got != tt.want {
				t.Errorf("buildVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
