// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestPublicationObjectsPreserveCompleteFormatsAfterSourceReplacement(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) string
	}{
		{"executable and SBOM", func(t *testing.T) string { root, _ := releasePublicationFixture(t); return root }},
		{"disk and executable", func(t *testing.T) string { return pullExecutableFixture(t).root }},
		{"Helm and executable", func(t *testing.T) string { return packagePullFixture(t, outputFormatHelm, "exact").root }},
		{"APT and executable", func(t *testing.T) string { return packagePullFixture(t, "apt-repository", "exact").root }},
		{"native OCI", func(t *testing.T) string {
			root, _, _ := heldNativeImageFixture(t, types.OCIManifestSchema1)
			return root
		}},
		{"native Docker", func(t *testing.T) string {
			root, _, _ := heldNativeImageFixture(t, types.DockerManifestSchema2)
			return root
		}},
		{"aggregate image", func(t *testing.T) string { root, _, _ := registryImageIndexFixture(t); return root }},
		{"generic bundle", func(t *testing.T) string {
			request, _ := bundlePublicationRequest(t)
			root := filepath.Join(t.TempDir(), "publication")
			if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"attested evidence", func(t *testing.T) string {
			root, _ := releasePublicationFixture(t)
			attested := filepath.Join(t.TempDir(), "attested")
			if _, err := AttestPreparedEvidence(t.Context(), root, attested, func(_ context.Context, subject string, raw []byte) ([]byte, error) {
				return imageIndexFixtureAttestation(subject, raw)
			}); err != nil {
				t.Fatal(err)
			}
			return attested
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := test.fixture(t)
			_, raw, err := readIndex(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			pin := digestBytes(raw)
			publication, err := OpenPublication(t.Context(), source, pin)
			if err != nil {
				t.Fatal(err)
			}
			defer func(owner *Publication) { _ = owner.Close() }(publication)
			expected := make(map[string][]byte)
			err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				key, err := filepath.Rel(source, path)
				if err != nil {
					return err
				}
				expected[filepath.ToSlash(key)], err = os.ReadFile(path)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(source, source+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "index.json"), []byte("substituted"), 0o600); err != nil {
				t.Fatal(err)
			}
			var keys []string
			restored := t.TempDir()
			err = publication.VisitObjects(t.Context(), func(ctx context.Context, object PublicationObject, body io.ReadSeeker) error {
				if ctx != t.Context() {
					t.Fatal("visitor changed context")
				}
				if _, ok := body.(interface{ objectStoreHeldBody() }); !ok {
					t.Fatal("reader does not retain its held-source capability")
				}
				got, err := io.ReadAll(body)
				if err != nil {
					return err
				}
				if !bytes.Equal(got, expected[object.Key]) || object.SHA256 != digestBytes(got) || object.Size != int64(len(got)) || object.MediaType == "" {
					t.Fatalf("object %s changed bytes or identity", object.Key)
				}
				if _, err := body.Seek(0, io.SeekStart); err != nil {
					return err
				}
				replay, err := io.ReadAll(body)
				if err != nil {
					return err
				}
				if !bytes.Equal(got, replay) {
					t.Fatalf("retry changed %s", object.Key)
				}
				keys = append(keys, object.Key)
				out := filepath.Join(restored, filepath.FromSlash(object.Key))
				if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
					return err
				}
				return os.WriteFile(out, got, 0o600)
			})
			if err != nil {
				t.Fatal(err)
			}
			want := make([]string, 0, len(expected))
			for key := range expected {
				want = append(want, key)
			}
			sort.Strings(want)
			if !reflect.DeepEqual(keys, want) {
				t.Fatalf("closure omitted or duplicated objects: got %v, want %v", keys, want)
			}
			checked, err := Inspect(t.Context(), restored)
			if err != nil || checked.RootDigest != pin {
				t.Fatalf("streamed closure is not independently inspectable: %v", err)
			}
		})
	}
}

func TestPublicationObjectsLendOneRevocableSeekableReaderAtATime(t *testing.T) {
	root, pin := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publication.Close() }()
	var previous io.ReadSeeker
	err = publication.VisitObjects(t.Context(), func(_ context.Context, object PublicationObject, body io.ReadSeeker) error {
		if previous != nil {
			_, err := previous.Read(make([]byte, 1))
			assertRegistryCode(t, err, e.CodeGateUnmet)
			_, err = previous.Seek(0, io.SeekStart)
			assertRegistryCode(t, err, e.CodeGateUnmet)
		}
		previous = body
		end, err := body.Seek(0, io.SeekEnd)
		if err != nil || end != object.Size {
			t.Fatalf("size probe changed pinned size: %d, %v", end, err)
		}
		if _, err := body.Seek(-1, io.SeekStart); err == nil {
			t.Fatal("negative seek accepted")
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return err
		}
		first := make([]byte, 1)
		if _, err := io.ReadFull(body, first); err != nil {
			return err
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return err
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		if len(raw) == 0 || raw[0] != first[0] || digestBytes(raw) != object.SHA256 {
			t.Fatal("size probe or partial read corrupted replay")
		}
		// Detached metadata may be edited without modifying the lent bytes.
		object.SHA256 = "changed"
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicationObjectsRejectUnavailableOwnersBeforeCallback(t *testing.T) {
	for _, scenario := range []struct {
		name string
		code e.Code
	}{
		{"nil", e.CodeGateUnmet}, {"unopened", e.CodeGateUnmet}, {"closed copy", e.CodeGateUnmet},
		{"nil visitor", e.CodeUsage}, {"nil context", e.CodeUsage}, {"canceled", e.CodeTimeout},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root, pin := releasePublicationFixture(t)
			publication, err := OpenPublication(t.Context(), root, pin)
			if err != nil {
				t.Fatal(err)
			}
			defer func(owner *Publication) { _ = owner.Close() }(publication)
			ctx := t.Context()
			calls := 0
			visit := func(context.Context, PublicationObject, io.ReadSeeker) error { calls++; return nil }
			switch scenario.name {
			case "nil":
				publication = nil
			case "unopened":
				publication = &Publication{}
			case "closed copy":
				copyOf := *publication
				if err := copyOf.Close(); err != nil {
					t.Fatal(err)
				}
			case "nil visitor":
				visit = nil
			case "nil context":
				ctx = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			assertRegistryCode(t, publication.VisitObjects(ctx, visit), scenario.code)
			if calls != 0 {
				t.Fatal("unavailable owner invoked transport callback")
			}
		})
	}
}

func TestPublicationObjectsRevokeReadersAndPreserveCallbackFailures(t *testing.T) {
	for _, scenario := range []string{"success", "dedup without read", "callback failure", "canceled callback", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			root, pin := releasePublicationFixture(t)
			publication, err := OpenPublication(t.Context(), root, pin)
			if err != nil {
				t.Fatal(err)
			}
			defer func(owner *Publication) { _ = owner.Close() }(publication)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var retained io.ReadSeeker
			calls := 0
			failure := e.New(e.CodeUnavailable, "synthetic transport failure")
			visit := func(_ context.Context, _ PublicationObject, body io.ReadSeeker) error {
				retained = body
				calls++
				switch scenario {
				case "callback failure":
					return failure
				case "canceled callback":
					cancel()
					_, _ = io.ReadAll(body)
					return nil
				case "panic":
					panic("synthetic callback panic")
				case "dedup without read":
					return nil
				default:
					_, err := io.Copy(io.Discard, body)
					return err
				}
			}
			if scenario == "panic" {
				func() {
					defer func() {
						if recover() == nil {
							t.Error("callback panic disappeared")
						}
					}()
					_ = publication.VisitObjects(ctx, visit)
				}()
			} else {
				err = publication.VisitObjects(ctx, visit)
				switch scenario {
				case "callback failure":
					if !errors.Is(err, failure) || calls != 1 {
						t.Fatalf("callback failure lost or visitation continued: %v, calls=%d", err, calls)
					}
					assertRegistryCode(t, err, e.CodeUnavailable)
				case "canceled callback":
					assertRegistryCode(t, err, e.CodeTimeout)
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation cause lost: %v", err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if retained == nil {
				t.Fatal("callback not invoked")
			}
			_, err = retained.Read(make([]byte, 1))
			assertRegistryCode(t, err, e.CodeGateUnmet)
			_, err = retained.Seek(0, io.SeekStart)
			assertRegistryCode(t, err, e.CodeGateUnmet)
		})
	}
}

func TestPublicationObjectsCloseJoinsActiveVisitor(t *testing.T) {
	root, pin := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer func(owner *Publication) { _ = owner.Close() }(publication)
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	visited := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		visited <- publication.VisitObjects(ctx, func(_ context.Context, _ PublicationObject, body io.ReadSeeker) error {
			close(entered)
			<-release
			_, err := io.Copy(io.Discard, body)
			cancel()
			return err
		})
	}()
	<-entered
	copyOf := *publication
	go func() { closed <- copyOf.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close released active owner: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	assertRegistryCode(t, <-visited, e.CodeTimeout)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestPublicationObjectsRejectChangedHeldDiskAndSwallowedReadFailure(t *testing.T) {
	for _, scenario := range []string{"wrong bytes", "truncated", "extended"} {
		t.Run(scenario, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "arm64")
			root := filepath.Join(t.TempDir(), "publication")
			result, err := BuildPublicationRoot(t.Context(), root, request)
			if err != nil {
				t.Fatal(err)
			}
			publication, err := OpenPublication(t.Context(), root, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer func(owner *Publication) { _ = owner.Close() }(publication)
			// Replace only the private test-owned held descriptor, not its checked
			// output metadata. External callers have no access to this map.
			disk := request.Objects[0].Output.Disk
			raw, err := io.ReadAll(io.NewSectionReader(publication.diskPayloads[disk.PayloadDigest], 0, disk.Size))
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong bytes":
				raw[0] ^= 0xff
			case "truncated":
				raw = raw[:len(raw)-1]
			case "extended":
				raw = append(raw, 0)
			}
			substitute := filepath.Join(t.TempDir(), "substitute")
			if err := os.WriteFile(substitute, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(substitute)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			for digest := range publication.diskPayloads {
				publication.diskPayloads[digest] = file
			}
			err = publication.VisitObjects(t.Context(), func(_ context.Context, _ PublicationObject, body io.ReadSeeker) error {
				// A buggy callback cannot turn a detected stream failure into success.
				_, _ = io.Copy(io.Discard, body)
				return nil
			})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}
