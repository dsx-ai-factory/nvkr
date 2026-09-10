package transport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type failingObjectWriter struct{ cancel context.CancelFunc }

func (w failingObjectWriter) Write([]byte) (int, error) {
	if w.cancel != nil {
		w.cancel()
	}
	return 0, errors.New("destination stopped accepting bytes")
}

func TestObjectStoreRoundTripAndReadFailures(t *testing.T) {
	store := (Seams{}).Resolve().Object
	target := Target{Ref: filepath.Join(t.TempDir(), "objects")}
	payload := []byte("exact published object\n")
	key := "nested/object"
	if err := store.Put(t.Context(), target, key, digestBytes(payload), bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := store.Get(t.Context(), target, key, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("round trip changed bytes: %q", got.Bytes())
	}
	for _, tc := range []struct {
		name                                        string
		canceled, missing, writerFails, cancelWrite bool
		want                                        e.Code
	}{
		{name: "canceled before read", canceled: true, want: e.CodeTimeout},
		{name: "missing object", missing: true, want: e.CodeInternal},
		{name: "writer fails", writerFails: true, want: e.CodeInternal},
		{name: "canceled during write", writerFails: true, cancelWrite: true, want: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			selected := key
			if tc.missing {
				selected = "missing"
			}
			var output bytes.Buffer
			var err error
			if tc.writerFails {
				writer := failingObjectWriter{}
				if tc.cancelWrite {
					writer.cancel = cancel
				}
				err = store.Get(ctx, target, selected, writer)
			} else {
				err = store.Get(ctx, target, selected, &output)
			}
			if code(err) != tc.want {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
			if output.Len() != 0 {
				t.Fatalf("failed admission emitted %q", output.String())
			}
		})
	}
}

func TestFilesystemAdaptersRejectInvalidIdentityBeforeOutput(t *testing.T) {
	for _, kind := range []string{"OCI", "package", "object", "referrer subject", "referrer blob", "referrer type"} {
		t.Run(kind, func(t *testing.T) {
			target := Target{Ref: filepath.Join(t.TempDir(), "absent")}
			seams := (Seams{}).Resolve()
			var err error
			want := e.CodeUnverifiedRef
			switch kind {
			case "OCI":
				_, err = seams.OCI.Push(t.Context(), target, "missing", "invalid")
			case "package":
				err = seams.Package.Publish(t.Context(), target, "missing", "invalid")
			case "object":
				err = seams.Object.Put(t.Context(), target, "key", "invalid", bytes.NewReader(nil))
			case "referrer subject":
				err = seams.OCI.AttachReferrer(t.Context(), target, "invalid", "type", "missing", fixedDigest('a'))
			case "referrer blob":
				err = seams.OCI.AttachReferrer(t.Context(), target, fixedDigest('a'), "type", "missing", "invalid")
			case "referrer type":
				err = seams.OCI.AttachReferrer(t.Context(), target, fixedDigest('a'), " ", "missing", fixedDigest('b'))
				want = e.CodeInvalidCatalog
			}
			if code(err) != want {
				t.Fatalf("error = %v, want %s", err, want)
			}
			if _, err := os.Lstat(target.Ref); !os.IsNotExist(err) {
				t.Fatalf("rejected identity touched destination: %v", err)
			}
		})
	}
}
