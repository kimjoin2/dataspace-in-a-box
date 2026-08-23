package main

import "runtime/debug"

// shortRevisionLen is how much of a commit hash identifies a build in
// practice. Seven is what git itself abbreviates to, so a version string and
// a `git log --oneline` line can be compared by eye.
const shortRevisionLen = 7

// buildVersion renders the identity of this build from what the toolchain
// stamped into it. There is no -ldflags version variable to keep in step with
// a tag: the toolchain already records both the module version and the VCS
// revision, and a value it fills in cannot drift from the build it describes.
//
// The two sources do not overlap, which is what makes the order below safe.
// VCS settings are stamped only when building from a working tree, and there
// the module version is a pseudo-version (v0.0.0-<date>-<rev>) rather than a
// tag — long, and carrying a twelve-character revision that does not line up
// with `git log --oneline`. So a working-tree build reports the short
// revision instead, marked when the tree had uncommitted changes. A build
// installed from a module version — `go install ...@v0.3.1` — carries that
// tag and no VCS settings at all, and reports the tag.
//
// A build with neither reports "unknown" rather than an empty string, because
// this value goes in a log line and an operator reading it needs to see that
// the answer is missing rather than that the field is.
//
// It takes debug.ReadBuildInfo's two return values rather than calling it,
// so the formatting can be tested without building a binary per case.
func buildVersion(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return "unknown"
	}

	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision != "" {
		if len(revision) > shortRevisionLen {
			revision = revision[:shortRevisionLen]
		}
		if modified {
			return revision + "-dirty"
		}
		return revision
	}

	// "(devel)" is what the toolchain records when no version can be
	// resolved; with no VCS stamp either, there is nothing left to report.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return "unknown"
}
