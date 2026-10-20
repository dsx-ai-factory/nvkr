// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package validation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestCatalogRefusesSymlinkDocumentsAndFIFOs(t *testing.T) {
	for _, kind := range []string{"index", "profile", "entry"} {
		for _, special := range []string{"symlink", "fifo"} {
			t.Run(kind+"/"+special, func(t *testing.T) {
				catalog, profile := catalogFixture(t)
				path := catalog
				if kind == "profile" {
					path = profile
				}
				if kind == "entry" {
					path = filepath.Join(filepath.Dir(catalog), "nvke/nvkectl/contract.yaml")
				}
				original := path + ".original"
				if err := os.Rename(path, original); err != nil {
					t.Fatal(err)
				}
				if special == "symlink" {
					if err := os.Symlink(original, path); err != nil {
						t.Fatal(err)
					}
				} else if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				admitted, executed := []string{}, []string{}
				done := make(chan error, 1)
				go func() {
					if kind == "profile" {
						_, err := admitProfile(ctx, profile, Runtime{}, map[string]adapter{"nvke-cli/v1": recordingCatalogAdapter{admitted: &admitted, executed: &executed}})
						done <- err
						return
					}
					_, err := LoadCatalog(ctx, catalog)
					done <- err
				}()
				select {
				case err := <-done:
					catalogCode(t, err, e.CodeUsage)
					if special == "fifo" && !strings.Contains(err.Error(), "bounded regular file") {
						t.Fatal("FIFO was not refused before reading", err)
					}
					if len(admitted) != 0 || len(executed) != 0 {
						t.Fatal("special profile reached adapter preparation")
					}
				case <-ctx.Done():
					// Unblock a regressed blocking FIFO open and join the reader before failing.
					if special == "fifo" {
						fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
						if err == nil {
							_ = unix.Close(fd)
						}
					}
					<-done
					t.Fatal("special document read did not refuse promptly")
				}
			})
		}
	}
}

func TestCatalogRootSymlinkRestrictionAndHardLinkScope(t *testing.T) {
	catalog, _ := catalogFixture(t)
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(filepath.Dir(catalog), link); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCatalog(t.Context(), filepath.Join(link, "catalog.yaml"))
	catalogCode(t, err, e.CodeUsage)
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatal("root restriction is not explained", err)
	}
	// Hard links are intentionally allowed; metadata loading claims no exclusive ownership.
	source := filepath.Join(filepath.Dir(catalog), "nvke/nvkectl/contract.yaml")
	alias := filepath.Join(t.TempDir(), "entry.yaml")
	if err := os.Link(source, alias); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(filepath.Dir(alias), "catalog.yaml")
	writeCatalogFile(t, index, []byte("apiVersion: nvkr.dev/validator-catalog/v1\nkind: ValidatorCatalog\nentries: [entry.yaml]\n"))
	c, err := LoadCatalog(t.Context(), index)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 1 {
		t.Fatal(c.List())
	}
}
