// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSelectedMetadataOwnsSparseBytesAndRevocation(t *testing.T) {
	full, pin := selectedMetadataFixtureWithSources(t, true)
	for _, selected := range []string{"node-image", "tools"} {
		t.Run(selected, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "selected")
			if _, err := CopySelectedMetadataWithExpectedRoot(t.Context(), full, root, pin, []string{selected}); err != nil {
				t.Fatal(err)
			}
			owner, err := OpenSelectedMetadata(t.Context(), root, pin, []string{selected}, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			shared := *owner
			files := slices.Clone(owner.state.files)
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			reference := "registry.example.test/releases@" + pin
			view, err := shared.Description(reference)
			if err != nil || len(view.Outputs) != 2 || view.RootDigest != pin {
				t.Fatalf("original logical membership = %+v, %v", view, err)
			}
			if !strings.Contains(view.ReferenceScope, "omitted evidence source may be unchecked") {
				t.Fatal("selected description lost its source-attribution scope")
			}
			unchecked, checked := 0, 0
			for _, evidence := range view.Evidence {
				if !slices.Contains(evidence.Binding.Outputs, selected) {
					unchecked++
					if evidence.Source != nil {
						t.Fatal("unselected evidence claims checked source attribution")
					}
				} else {
					checked++
					if evidence.Source == nil || evidence.Source.Repo != "https://example.test/"+selected+".git" || evidence.Source.Commit != strings.Repeat("a", 40) {
						t.Fatal("selected source-built evidence lost its checked attribution")
					}
				}
			}
			if unchecked == 0 || checked == 0 {
				t.Fatal("fixture did not retain unselected evidence")
			}
			view.Outputs[0].Name = "substituted"
			again, err := shared.Description(reference)
			if err != nil || again.Outputs[0].Name == "substituted" {
				t.Fatalf("description aliases owner: %v", err)
			}
			var borrowed io.ReadSeeker
			objects := 0
			err = shared.VisitObjects(t.Context(), func(_ context.Context, object PublicationObject, source io.ReadSeeker) error {
				objects++
				borrowed = source
				raw, err := io.ReadAll(source)
				if err != nil || int64(len(raw)) != object.Size || digestBytes(raw) != object.SHA256 || len(object.DiskOutputs) != 0 {
					t.Fatalf("held sparse object %s differs: %v", object.Key, err)
				}
				return nil
			})
			if err != nil || objects != len(files) {
				t.Fatalf("object visits = %d, want %d: %v", objects, len(files), err)
			}
			_, err = borrowed.Read(make([]byte, 1))
			assertRegistryCode(t, err, e.CodeGateUnmet)
			var evidence [][]byte
			for round := range 2 {
				visits := 0
				err = shared.VisitEvidence(t.Context(), func(_ context.Context, binding EvidenceBinding, raw []byte) error {
					if !slices.Contains(binding.Outputs, selected) || len(raw) == 0 {
						t.Fatal("selected evidence lost its exact named use")
					}
					if round == 0 {
						evidence = append(evidence, slices.Clone(raw))
					} else if visits >= len(evidence) || !bytes.Equal(raw, evidence[visits]) {
						t.Fatal("evidence bytes changed after mutating a previous visit")
					}
					binding.Outputs[0] = "mutated"
					raw[0] = '!'
					visits++
					return nil
				})
				if err != nil || visits == 0 || visits != len(evidence) {
					t.Fatalf("evidence snapshot: visits=%d err=%v", visits, err)
				}
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = shared.Description(reference)
			assertRegistryCode(t, err, e.CodeGateUnmet)
			for _, file := range files {
				if file.OSFile() != nil {
					t.Fatal("Close leaked retained metadata bytes")
				}
			}
		})
	}
}

func TestSelectedMetadataCloseJoinsActiveEvidence(t *testing.T) {
	full, pin := selectedMetadataFixture(t)
	root := filepath.Join(t.TempDir(), "selected")
	if _, err := CopySelectedMetadataWithExpectedRoot(t.Context(), full, root, pin, []string{"tools"}); err != nil {
		t.Fatal(err)
	}
	owner, err := OpenSelectedMetadata(t.Context(), root, pin, []string{"tools"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	entered, release := make(chan struct{}), make(chan struct{})
	visited := make(chan struct{})
	var group sync.WaitGroup
	group.Go(func() {
		defer close(visited)
		if err := owner.VisitEvidence(t.Context(), func(_ context.Context, _ EvidenceBinding, _ []byte) error {
			close(entered)
			<-release
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-entered:
	case <-visited:
		t.Fatal("evidence visit ended before entering its callback")
	}
	closed := make(chan struct{})
	group.Go(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
		close(closed)
	})
	closedEarly := false
	// The callback holds a read lock. A failed TryRLock proves the closer
	// has reached its pending write lock; a scheduler yield alone cannot.
	for owner.state.mu.TryRLock() {
		owner.state.mu.RUnlock()
		select {
		case <-closed:
			closedEarly = true
		default:
			runtime.Gosched()
		}
		if closedEarly {
			break
		}
	}
	select {
	case <-closed:
		closedEarly = true
	default:
	}
	close(release)
	group.Wait()
	if closedEarly {
		t.Fatal("Close returned while evidence callback held the owner")
	}
	err = owner.VisitEvidence(t.Context(), func(context.Context, EvidenceBinding, []byte) error { return nil })
	assertRegistryCode(t, err, e.CodeGateUnmet)
}

func TestSelectedMetadataObjectTransferReinspectsWithoutPayloads(t *testing.T) {
	full, pin := selectedMetadataFixture(t)
	selected := filepath.Join(t.TempDir(), "selected")
	if _, err := CopySelectedMetadataWithExpectedRoot(t.Context(), full, selected, pin, []string{"tools"}); err != nil {
		t.Fatal(err)
	}
	owner, err := OpenSelectedMetadata(t.Context(), selected, pin, []string{"tools"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	out := t.TempDir()
	if err := owner.VisitObjects(t.Context(), func(_ context.Context, object PublicationObject, reader io.ReadSeeker) error {
		path := filepath.Join(out, object.Key)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, reader)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectSelectedMetadata(t.Context(), out, pin, []string{"tools"})
	if err != nil || inspection.RootDigest != pin {
		t.Fatal("transferred selected objects lost their exact root", err)
	}
}

func TestSelectedMetadataRejectsExtraBytesAndUnattestedEvidence(t *testing.T) {
	full, pin := releasePublicationFixture(t)
	root := filepath.Join(t.TempDir(), "selected")
	if _, err := CopySelectedMetadataWithExpectedRoot(t.Context(), full, root, pin, []string{"tools"}); err != nil {
		t.Fatal(err)
	}
	owner, err := OpenSelectedMetadata(t.Context(), root, pin, []string{"tools"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	called := false
	err = owner.VisitEvidence(t.Context(), func(context.Context, EvidenceBinding, []byte) error { called = true; return nil })
	assertRegistryCode(t, err, e.CodeProvenanceMissing)
	if called {
		t.Fatal("unattested evidence reached policy callback")
	}
	raw := []byte("unselected payload")
	if err := os.WriteFile(fixtureBlobPath(root, digestBytes(raw)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	other, err := OpenSelectedMetadata(t.Context(), root, pin, []string{"tools"}, "")
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if other != nil {
		t.Fatal("extra bytes issued metadata owner")
	}
}
