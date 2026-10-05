package main

import (
	"strings"
	"testing"
)

func TestReportAuthorityLabel(t *testing.T) {
	for _, test := range []struct {
		name      string
		qualified bool
		profile   string
		want      string
	}{
		{name: "development", want: "UNQUALIFIED DEVELOPMENT OUTPUT"},
		{name: "acquired", qualified: true, want: "verified acquired bytes, no execution qualification or reproduction claim"},
		{name: "execution", qualified: true, profile: "sha256:profile", want: "qualified execution (sha256:profile)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			label := reportAuthorityLabel(test.qualified, test.profile)
			if !strings.HasPrefix(label, test.want) {
				t.Fatalf("label %q; want %q", label, test.want)
			}
		})
	}
}
