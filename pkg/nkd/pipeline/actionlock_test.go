package pipeline

import (
	"context"
	"strings"
	"testing"
)

func TestEmbeddedActionLockIsValidAndDrivesEmitters(t *testing.T) {
	lock := EmbeddedActionLock()
	if err := lock.Validate(); err != nil {
		t.Fatalf("embedded action lock invalid: %v", err)
	}
	// The exported emitter refs MUST equal the lockfile pins: the whole point is that data,
	// not a code constant, is the source of truth, so the two must never drift.
	for name, uses := range map[string]string{
		"checkout":          GitHubCheckoutAction,
		"upload-artifact":   GitHubUploadAction,
		"download-artifact": GitHubDownloadAction,
		"setup-go":          GitHubSetupGoAction,
	} {
		pin, err := lock.Pin(name)
		if err != nil {
			t.Fatalf("lock missing %q: %v", name, err)
		}
		if pin.Uses() != uses {
			t.Fatalf("emitter ref for %q = %q, lockfile = %q", name, uses, pin.Uses())
		}
	}
}

func TestActionLockMarshalRoundTrips(t *testing.T) {
	// The committed embedded bytes must be EXACTLY what Marshal produces, so a `bump` rewrite
	// is a minimal, reviewable diff and never silently reformats the file.
	got, err := EmbeddedActionLock().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(embeddedActionLock) {
		t.Fatalf("embedded lockfile is not canonical Marshal output:\n--- committed ---\n%s\n--- marshal ---\n%s", embeddedActionLock, got)
	}
}

func TestParseActionLockRejectsUnpinned(t *testing.T) {
	raw := []byte("apiVersion: nkd.dev/lock/v1\nkind: ActionLock\nactions:\n  checkout:\n    repo: actions/checkout\n    track: v4\n    version: v4.3.1\n    sha: v4.3.1\n")
	if _, err := ParseActionLock(raw); err == nil {
		t.Fatal("an action pinned to a tag rather than a 40-hex sha must be rejected")
	}
}

type fakeResolver map[string][2]string // "repo@track" -> {sha, version}

func (f fakeResolver) Resolve(_ context.Context, repo, track string) (string, string, error) {
	v := f[repo+"@"+track]
	return v[0], v[1], nil
}

func TestActionLockBumpMovesOnlyChangedPins(t *testing.T) {
	lock := EmbeddedActionLock()
	newSHA := strings.Repeat("a", 40)
	res := fakeResolver{
		"actions/checkout@v4":          {lock.Actions["checkout"].SHA, "v4.3.1"},
		"actions/upload-artifact@v4":   {lock.Actions["upload-artifact"].SHA, "v4.6.2"},
		"actions/download-artifact@v4": {lock.Actions["download-artifact"].SHA, "v4.3.0"},
		"actions/setup-go@v5":          {newSHA, "v5.7.0"},
	}
	bumped, changes, err := lock.Bump(context.Background(), res, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Name != "setup-go" || changes[0].NewSHA != newSHA {
		t.Fatalf("expected exactly one setup-go change, got %+v", changes)
	}
	if bumped.Actions["setup-go"].SHA != newSHA || bumped.Actions["setup-go"].Version != "v5.7.0" {
		t.Fatalf("setup-go not bumped: %+v", bumped.Actions["setup-go"])
	}
	if err := bumped.Validate(); err != nil {
		t.Fatalf("bumped lock invalid: %v", err)
	}
}
