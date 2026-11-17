// Copyright (c) QuantumNous and contributors.
package common

import (
	"regexp"
	"runtime/debug"
)

var sourceCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// BuildSourceRevision uses compiler-recorded VCS data, never VERSION or an
// operator-supplied environment variable. Missing or modified sources are unknown.
func BuildSourceRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "UNKNOWN"
	}
	return sourceRevisionFromBuildInfo(info)
}

func sourceRevisionFromBuildInfo(info *debug.BuildInfo) string {
	var vcs, revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs":
			vcs = setting.Value
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if vcs != "git" || modified != "false" || !sourceCommitPattern.MatchString(revision) {
		return "UNKNOWN"
	}
	return revision
}
