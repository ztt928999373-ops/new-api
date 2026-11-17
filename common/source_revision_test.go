// Copyright (c) QuantumNous and contributors.
package common

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceRevisionRequiresCleanCompilerVCSData(t *testing.T) {
	revision := strings.Repeat("a", 40)
	for _, tc := range []struct{ name, vcs, revision, modified, want string }{
		{"clean", "git", revision, "false", revision},
		{"dirty", "git", revision, "true", "UNKNOWN"},
		{"missing modified marker", "git", revision, "", "UNKNOWN"},
		{"non git", "hg", revision, "false", "UNKNOWN"},
		{"short revision", "git", "abcdef0", "false", "UNKNOWN"},
		{"missing metadata", "", "", "", "UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VERSION", revision)
			t.Setenv("SOURCE_REVISION", revision)
			info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs", Value: tc.vcs}, {Key: "vcs.revision", Value: tc.revision}, {Key: "vcs.modified", Value: tc.modified}}}
			require.Equal(t, tc.want, sourceRevisionFromBuildInfo(info))
		})
	}
}
