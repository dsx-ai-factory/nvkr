// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestConcurrentBundleVerificationIsolatesTUFCache(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			const workers = 4
			ambient := t.TempDir()
			environment := []string{"HOME=" + ambient, "USERPROFILE=" + ambient, "XDG_CACHE_HOME=" + ambient,
				"TUF_ROOT=" + ambient, "SIGSTORE_ROOT_FILE=untrusted", "PATH=/synthetic/bin"}
			original := slices.Clone(environment)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := make(chan string, workers)
			release := make(chan struct{})
			results := make(chan error, workers)
			for i := range workers {
				go func() {
					// Separate runner records avoid sharing the test stub's call slice.
					runner := &cosignStub{onCall: func(call recordedCall) error {
						var home string
						for _, entry := range call.env {
							if value, ok := strings.CutPrefix(entry, "HOME="); ok {
								home = value
							}
							if strings.HasPrefix(entry, "TUF_") || strings.HasPrefix(entry, "SIGSTORE_") || strings.HasPrefix(entry, "XDG_") {
								return fmt.Errorf("ambient override reached verifier: %s", entry)
							}
						}
						info, err := os.Stat(home)
						if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || home == ambient {
							return fmt.Errorf("verifier lacks private HOME: %q, %v", home, err)
						}
						cache := filepath.Join(home, ".sigstore", "root", "tuf-repo-cdn.sigstore.dev")
						if err := os.MkdirAll(cache, 0700); err != nil {
							return err
						}
						path := filepath.Join(cache, "root.json")
						data := fmt.Sprintf("synthetic root for worker %d", i)
						if err := os.WriteFile(path, []byte(data), 0600); err != nil {
							return err
						}
						started <- home
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-release:
						}
						read, err := os.ReadFile(path)
						if err != nil || string(read) != data {
							return fmt.Errorf("concurrent verifier replaced cache metadata: %v", err)
						}
						if outcome == "failure" {
							return errors.New("synthetic verification failure")
						}
						return nil
					}}
					signer := &CosignOCISigner{runner: runner, binary: "cosign"}
					_, err := signer.capture(ctx, []string{"verify-blob-attestation"}, environment)
					results <- err
				}()
			}
			var homes []string
			for range workers {
				select {
				case home := <-started:
					homes = append(homes, home)
				case <-ctx.Done():
					// Join every worker even if setup fails before the barrier.
					cancel()
				}
			}
			if outcome == "canceled" {
				cancel()
			} else {
				close(release)
			}
			errorsByWorker := make([]error, workers)
			for i := range workers {
				errorsByWorker[i] = <-results
			}
			for _, err := range errorsByWorker {
				switch outcome {
				case "success":
					if err != nil {
						t.Error(err)
					}
				case "failure":
					requireSignCode(t, err, e.CodeSignatureInvalid)
				case "canceled":
					requireSignCode(t, err, e.CodeTimeout)
				}
			}
			slices.Sort(homes)
			if len(slices.Compact(slices.Clone(homes))) != workers {
				t.Fatalf("verifications did not receive unique homes: %v", homes)
			}
			for _, home := range homes {
				if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("verification cache survived child completion: %q, %v", home, err)
				}
			}
			entries, err := os.ReadDir(ambient)
			if err != nil || len(entries) != 0 || !slices.Equal(environment, original) {
				t.Fatal("verification changed ambient cache or caller environment", err)
			}
		})
	}
}
