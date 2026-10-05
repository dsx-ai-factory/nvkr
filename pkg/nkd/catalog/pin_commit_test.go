package catalog

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestResolveCommitPin(t *testing.T) {
	repo, original, _ := pinTreeFixture(t, map[string][]byte{".go-version": []byte("1.27.1\n")})
	source := Source{Repo: repo, TagPrefix: "module/"}
	got, err := ResolveCommitPin(t.Context(), source, original.Commit)
	if err != nil {
		t.Fatal(err)
	}
	want := original
	want.Tag = original.Commit
	want.GoVersion = "1.27.1"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commit pin %+v; want exact held metadata %+v", got, want)
	}
}

func TestCommitPinRejectsUnsupportedSelectionBeforeFetch(t *testing.T) {
	for _, test := range []struct {
		name   string
		source Source
		commit string
	}{
		{name: "abbreviated", commit: "abcdef0"},
		{name: "tag", commit: "v1.2.3"},
		{name: "action", source: Source{Kind: SourceGitHubAction, Repo: "https://github.com/actions/checkout"}, commit: strings.Repeat("a", 40)},
		{name: "tool", source: Source{Tool: &ToolSource{}}, commit: strings.Repeat("a", 40)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveCommitPin(t.Context(), test.source, test.commit)
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
		})
	}
}

func TestCommitPinUpdateCheckAndCommit(t *testing.T) {
	repo, original, _ := pinTreeFixture(t, map[string][]byte{"readme": []byte("source bytes")})
	for _, check := range []bool{true, false} {
		t.Run(map[bool]string{true: "check", false: "commit"}[check], func(t *testing.T) {
			dir := sourceUpdateFixture(t)
			before := readPinFixtureLock(t, dir)
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "url."+repo+".insteadOf")
			t.Setenv("GIT_CONFIG_VALUE_0", "https://example.invalid/source")
			got, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: dir, Sources: []string{"source"}, Commit: original.Commit, Check: check})
			if check {
				assertCatalogContractCode(t, err, e.CodeDiff)
			} else if err != nil {
				t.Fatal(err)
			}
			if got.Changed != 1 || got.Pins[0].After.Commit != original.Commit {
				t.Fatalf("update %+v", got)
			}
			if bytes.Equal(before, readPinFixtureLock(t, dir)) != check {
				t.Fatal("lock write disagrees with check mode")
			}
		})
	}
}

func TestCommitPinUpdateAdmission(t *testing.T) {
	for _, request := range []SourcePinRequest{
		{Commit: "short"},
		{Commit: strings.Repeat("a", 40), Tag: "v1.0.0"},
		{Commit: strings.Repeat("a", 40), Bump: true},
	} {
		_, err := UpdateSourcePins(context.Background(), request)
		assertCatalogContractCode(t, err, e.CodeUsage)
	}
}
