// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSourceOverrideIsCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, test := range []struct {
		value  string
		commit bool
		valid  bool
	}{
		{commit, true, true},
		{"1.2.x", false, true},
		{"v0.22.0", false, true},
		{"7.x", false, true},
		{"*", false, true},
		{"1", false, true},
		{"", false, false},
		{strings.ToUpper(commit), false, false},
		{commit[:7], false, false},
		{"main", false, false},
		{"refs/heads/main", false, false},
		{"1..2", false, false},
		{"1.2.3.4", false, false},
		{"01.2", false, false},
		{">=1.2", false, false},
		{"1.2.x;true", false, false},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := SourceOverrideIsCommit(test.value)
			if !test.valid {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
					t.Fatalf("got %v, want %s", err, e.CodeUsage)
				}
				return
			}
			if err != nil || got != test.commit {
				t.Fatalf("got %v %v, want %v", got, err, test.commit)
			}
		})
	}
}

func TestSourceTagAdmitted(t *testing.T) {
	for _, test := range []struct {
		prefix, constraint, tag string
		want                    bool
	}{
		{"", "1.2.x", "v1.2.7", true},
		{"", "1.2.x", "v1.3.0", false},
		{"", "v0.22.0", "v0.22.0", true},
		{"", "1.2.x", "v1.2.7-rc.1", false},
		{"cli/", "0.1.x", "cli/v0.1.4", true},
		{"cli/", "0.1.x", "v0.1.4", false},
		{"", "1.2.x", "cli/v1.2.0", false},
		{"", "bogus", "v1.2.0", false},
	} {
		if got := SourceTagAdmitted(test.prefix, test.constraint, test.tag); got != test.want {
			t.Errorf("SourceTagAdmitted(%q, %q, %q) = %v, want %v", test.prefix, test.constraint, test.tag, got, test.want)
		}
	}
}

func TestSourceOverrideResolutionRejectsBeforeNetwork(t *testing.T) {
	commit := strings.Repeat("b", 40)
	for _, test := range []struct {
		name string
		call func() error
		code e.Code
	}{
		{"constraint is commit", func() error {
			_, err := ResolveSourceConstraint(t.Context(), "https://github.com/org/repo", "", commit)
			return err
		}, e.CodeUsage},
		{"malformed constraint", func() error {
			_, err := ResolveSourceConstraint(t.Context(), "https://github.com/org/repo", "", "latest")
			return err
		}, e.CodeUsage},
		{"local repository", func() error {
			_, err := ResolveSourceConstraint(t.Context(), "file:///tmp/repo", "", "1.x")
			return err
		}, e.CodeInvalidCatalog},
		{"replay without commit", func() error {
			_, err := ResolveSourceTag(t.Context(), "https://github.com/org/repo", "", "v1.0.0", "")
			return err
		}, e.CodeUnpinnedSource},
		{"replay commit as tag", func() error {
			_, err := ResolveSourceTag(t.Context(), "https://github.com/org/repo", "", commit, commit)
			return err
		}, e.CodeUnpinnedSource},
		{"replay local repository", func() error {
			_, err := ResolveSourceTag(t.Context(), "file:///tmp/repo", "", "v1.0.0", commit)
			return err
		}, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			var coded *e.Error
			if err := test.call(); !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v, want %s", err, test.code)
			}
		})
	}
}
