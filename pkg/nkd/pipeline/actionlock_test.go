package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
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

func TestActionLockRetargetPersistsTrackOnlyChanges(t *testing.T) {
	lock := EmbeddedActionLock()
	oldTrack := lock.Actions["checkout"].Track
	bumped, changes, err := lock.Retarget(map[string]string{"checkout": "v99"})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Name != "checkout" || changes[0].OldTrack != oldTrack || changes[0].NewTrack != "v99" {
		t.Fatalf("track changes = %+v", changes)
	}
	if bumped.Actions["checkout"].Track != "v99" || lock.Actions["checkout"].Track != oldTrack {
		t.Fatalf("retarget did not return an independent updated lock")
	}
	for _, test := range []map[string]string{
		{"missing": "v7"},
		{"checkout": "main"},
	} {
		_, _, err := lock.Retarget(test)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
			t.Fatalf("invalid retarget %v = %v, want %s", test, err, e.CodeUsage)
		}
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
		"actions/checkout@v7":          {lock.Actions["checkout"].SHA, "v7.0.1"},
		"actions/upload-artifact@v7":   {lock.Actions["upload-artifact"].SHA, "v7.0.1"},
		"actions/download-artifact@v8": {lock.Actions["download-artifact"].SHA, "v8.0.1"},
		"actions/setup-go@v7":          {newSHA, "v7.0.1"},
	}
	bumped, changes, err := lock.Bump(context.Background(), res, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Name != "setup-go" || changes[0].NewSHA != newSHA {
		t.Fatalf("expected exactly one setup-go change, got %+v", changes)
	}
	if bumped.Actions["setup-go"].SHA != newSHA || bumped.Actions["setup-go"].Version != "v7.0.1" {
		t.Fatalf("setup-go not bumped: %+v", bumped.Actions["setup-go"])
	}
	if err := bumped.Validate(); err != nil {
		t.Fatalf("bumped lock invalid: %v", err)
	}
}
