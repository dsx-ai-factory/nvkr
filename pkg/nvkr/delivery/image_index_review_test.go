// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestImageIndexDestinationSeparators(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, trailing := range []bool{false, true} {
			name := "absent"
			if existing {
				name = "existing"
			}
			if trailing {
				name += "-slash"
			}
			t.Run(name, func(t *testing.T) {
				index := assembledImageIndex(t, imageIndexInputs(t))
				out := filepath.Join(t.TempDir(), "index")
				if existing {
					if err := os.Mkdir(out, 0700); err != nil {
						t.Fatal(err)
					}
				}
				requested := out
				if trailing {
					requested += string(filepath.Separator)
				}
				err := index.WriteLayout(t.Context(), requested)
				if existing {
					assertRegistryCode(t, err, e.CodeImmutableViolation)
					if len(mustReadImageDirectory(t, out)) != 0 {
						t.Fatal("existing destination modified")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				d, err := index.Description()
				if err != nil {
					t.Fatal(err)
				}
				opened, err := OpenImageIndex(t.Context(), out, d.Digest, imageIndexPlatforms)
				if err != nil {
					t.Fatal(err)
				}
				defer opened.Close()
			})
		}
	}
}

func TestImageIndexCopiedHandleCloseRace(t *testing.T) {
	for i := 0; i < 20; i++ {
		original := assembledImageIndex(t, imageIndexInputs(t))
		copied := *original
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_, _ = copied.Description()
			}
		}()
		go func() { defer wg.Done(); <-start; _ = original.Close() }()
		close(start)
		wg.Wait()
		_, err := copied.Description()
		assertRegistryCode(t, err, e.CodeVerifyFailed)
		out := filepath.Join(t.TempDir(), "revoked")
		assertRegistryCode(t, copied.WriteLayout(t.Context(), out), e.CodeVerifyFailed)
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("revoked copy wrote output: %v", err)
		}
		if err := copied.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImageAliasWorkspaceFailureCode(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	err := publishImageAliases(t.Context(), "unused", []imageAlias{{}}, RegistryOptions{})
	assertRegistryCode(t, err, e.CodeInternal)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost filesystem cause: %v", err)
	}
}
