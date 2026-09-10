package pipeline

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestTransportSnapshotRejectsChangedOrUnavailableSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"exact", ""}, {"closed", e.CodeVerifyFailed}, {"changed", e.CodeVerifyFailed},
		{"canceled", e.CodeTimeout}, {"existing destination", e.CodeInternal},
		{"missing source", e.CodeInternal}, {"missing metadata", e.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source")
			payload := []byte("exact archive bytes")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			held, info, err := openRegularNoFollow(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			destination := filepath.Join(t.TempDir(), "snapshot")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			source := held
			switch tc.name {
			case "closed":
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
			case "changed":
				if err := os.WriteFile(path, []byte("different and longer bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			case "existing destination":
				if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing source":
				source = nil
			case "missing metadata":
				info = nil
			}
			digest, snapshot, err := snapshotTransport(ctx, source, info, destination)
			if tc.code != "" {
				if transportCode(err) != tc.code || digest != "" || snapshot != nil {
					t.Fatalf("snapshot = %q, %v, %v; want %s", digest, snapshot, err, tc.code)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer snapshot.Close()
				raw, err := io.ReadAll(snapshot)
				if err != nil || !bytes.Equal(raw, payload) || digest != FileDigest(payload) {
					t.Fatalf("snapshot bytes %q, %v; digest %q", raw, err, digest)
				}
			}
			if tc.name == "existing destination" {
				raw, err := os.ReadFile(destination)
				if err != nil || string(raw) != "keep" {
					t.Fatalf("destination changed: %q, %v", raw, err)
				}
			} else if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatalf("snapshot path leaked: %v", err)
			}
			recheck, metadata, err := stableTransportDigest(ctx, source, info)
			if tc.name == "exact" || tc.name == "existing destination" {
				if err != nil || recheck != FileDigest(payload) || metadata == nil {
					t.Fatalf("recheck = %q, %v, %v", recheck, metadata, err)
				}
			} else if err == nil || recheck != "" || metadata != nil {
				t.Fatalf("invalid recheck returned identity: %q, %v, %v", recheck, metadata, err)
			}
		})
	}
}
