// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/sourcegit"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// fetch acquires pinned source into SourceRoot/Dest and verifies the commit and
// tree hash, failing closed on any mismatch (NKD-FR-BLD-022, NKD-FR-RC-023). An
// already-checked-out matching source closure is reused only through a re-hashed
// source receipt; HEAD alone is never sufficient.
func (l Local) fetch(n ir.Node, opts RunOptions) (returnErr error) {
	return l.fetchWithSourceVendorAdmission(n, opts, admitSourceVendor)
}

// The private core keeps the complete Git/closure lifecycle testable with a
// modeled daemon. The production entry above always supplies concrete admission.
func (l Local) fetchWithSourceVendorAdmission(n ir.Node, opts RunOptions,
	admitVendor func(context.Context, ir.Node, string) (*sourceVendor, error),
) (returnErr error) {
	var vendor *sourceVendor
	if opts.SourceWorkspace != nil {
		environment := opts.SourceGitEnvironment
		if environment == nil {
			var err error
			environment, err = sourcegit.Baseline(os.Getenv("PATH"))
			if err != nil {
				return err
			}
		}
		_, admitted, err := sourcegit.Admit(n.Repo, environment)
		if err != nil {
			return err
		}
		opts.SourceGitEnvironment, opts.fetchRepository = admitted, n.Repo
		ctx, workspace, release, err := opts.SourceWorkspace.borrow(opts.ctx())
		if err != nil {
			return err
		}
		defer release()
		opts.Ctx, opts.fetchWorkspace = ctx, workspace
		if len(n.VendorCmd) != 0 && !opts.DryRun {
			var err error
			vendor, err = admitVendor(opts.ctx(), n, opts.Runtime)
			if err != nil {
				return err
			}
			defer func() {
				if vendor.held == nil {
					return // The vendor callback closed its owner before receipt commit.
				}
				if err := vendor.held.close(); err != nil {
					returnErr = errors.Join(returnErr, e.Wrap(e.CodeFetchFailed, err, "closing source vendor runtime"))
				}
			}()
		}
	}
	dir, err := pathWithin(opts.SourceRoot, n.Dest)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "resolving fetch destination %q", n.Dest)
	}
	abs, _ := filepath.Abs(dir)

	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[fetch] %s @ %s -> %s (verify tree %s)\n", n.Repo, abbrev(n.Commit), abs, abbrev(n.TreeHash))
		return nil
	}
	if opts.SourceWorkspace != nil {
		checkout, err := holdSourceFetchWorkspaceDirectory(opts.ctx(), opts.fetchWorkspace, filepath.Join(opts.SourceRoot, n.Dest))
		if err != nil {
			return err
		}
		defer func() { _ = checkout.Close() }()
		opts.fetchDirectory = checkout
		receiptPath, err := sourceReceiptPath(opts.SourceRoot, n.Dest)
		if err != nil {
			return err
		}
		receipts, err := holdSourceFetchWorkspaceDirectory(opts.ctx(), opts.fetchWorkspace, filepath.Dir(receiptPath))
		if err != nil {
			return err
		}
		defer func() { _ = receipts.Close() }()
		opts.fetchReceiptDirectory = receipts
	}

	probeExisting := true
	if opts.fetchDirectory != nil {
		// A newly created held checkout has no repository yet. Git must not walk
		// upward and mistake the enclosing workflow checkout for this source.
		repository, _, err := securepath.OpenChild(opts.fetchDirectory, ".git", true)
		if os.IsNotExist(err) {
			probeExisting = false
			repository, _, err = securepath.CreateParent(opts.fetchDirectory, ".git/.nkd-held-directory")
			if err != nil {
				return e.Wrap(e.CodeFetchFailed, err, "creating held source Git directory")
			}
		} else if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "held source checkout has an unsafe Git directory")
		}
		opts.fetchGitDirectory = repository
		defer func() {
			if err := repository.Close(); err != nil {
				returnErr = errors.Join(returnErr, e.Wrap(e.CodeFetchFailed, err, "closing held source Git directory"))
			}
		}()
	}
	if probeExisting {
		head, err := sourceFetchGitOut(opts, dir, "rev-parse", "HEAD")
		if err == nil && head == n.Commit {
			if err := l.verifyTree(dir, n, opts); err != nil {
				return err
			}
			return l.completeFetch(n, dir, opts, vendor)
		}
	}

	// Fetch exactly the pinned commit (shallow). GitHub serves reachable SHAs.
	if opts.fetchDirectory == nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "creating source dir %s", dir)
		}
	}
	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", n.Repo},
		{"fetch", "-q", "--depth", "1", "origin", n.Commit},
		{"checkout", "-q", "--detach", "FETCH_HEAD"},
	}
	if opts.fetchDirectory != nil {
		// The held route fetches the selected source, never a retained origin.
		steps = [][]string{{"init", "-q"}, {"fetch", "-q", "--depth", "1", n.Repo, n.Commit}, {"checkout", "-q", "--detach", "FETCH_HEAD"}}
	}
	for _, s := range steps {
		if _, err := sourceFetchGitOut(opts, dir, s...); err != nil {
			if opts.ctx().Err() != nil {
				return err
			}
			// `remote add` fails if origin exists on a reused dir; tolerate.
			if s[0] == "remote" {
				continue
			}
			return e.Wrap(e.CodeFetchFailed, err, "git %s in %s", strings.Join(s, " "), dir)
		}
	}
	if err := l.verifyTree(dir, n, opts); err != nil {
		return err
	}
	return l.completeFetch(n, dir, opts, vendor)
}

func (l Local) completeFetch(n ir.Node, dir string, opts RunOptions, vendor *sourceVendor) error {
	if opts.SourceWorkspace == nil || len(n.VendorCmd) == 0 {
		return l.vendor(n, dir, opts)
	}
	return ensureSourceClosure(n, dir, opts, func() error {
		err := vendor.run(opts.ctx(), opts.fetchWorkspace, opts.fetchDirectory, opts.Stdout, opts.Stderr)
		// Runtime cleanup is part of the closure operation, not a post-commit
		// advisory failure. A failed Close cannot leave a newly issued receipt.
		if closeErr := vendor.held.close(); closeErr != nil {
			err = errors.Join(err, e.Wrap(e.CodeFetchFailed, closeErr, "closing source vendor runtime before receipt"))
		}
		vendor.held = nil
		return err
	})
}

// vendor completes the fetch closure. The optional vendor command runs in the
// toolchain container with network, after which ensureSourceClosure records and
// verifies the exact bytes consumed by the offline build.
func (l Local) vendor(n ir.Node, dir string, opts RunOptions) error {
	return ensureSourceClosure(n, dir, opts, func() error {
		abs, _ := filepath.Abs(dir)
		cname := containerName(ir.Node{ID: "vendor-" + n.Dest})
		args := append([]string{"run", "--rm", "--name", cname, "--network=" + ir.NetworkDefault, "-v", abs + ":/src", "-w", "/src", n.VendorImage}, n.VendorCmd...)
		fmt.Fprintf(opts.Stdout, "\n=== vendor %s (network, %s) ===\n", n.Dest, n.VendorImage)
		opts.progress.printf("[container] vendor %s — launching %s with network access\n", n.Dest, n.VendorImage)
		if err := registerContainer(opts, cname); err != nil {
			return err
		}
		if err := runContainerRuntime(opts, args...); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "vendoring %s", n.Dest)
		}
		opts.reap.forget(cname)
		return nil
	})
}

func (l Local) verifyTree(dir string, n ir.Node, opts RunOptions) error {
	head, err := sourceFetchGitOut(opts, dir, "rev-parse", "HEAD")
	if err != nil {
		if opts.ctx().Err() != nil {
			return err
		}
		return e.Wrap(e.CodeFetchFailed, err, "resolving HEAD in %s", dir)
	}
	if head != n.Commit {
		return e.New(e.CodeFetchFailed, "commit mismatch in %s: got %s want %s", dir, head, n.Commit)
	}
	tree, err := sourceFetchGitOut(opts, dir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		if opts.ctx().Err() != nil {
			return err
		}
		return e.Wrap(e.CodeFetchFailed, err, "resolving tree in %s", dir)
	}
	if tree != n.TreeHash {
		return e.New(e.CodeFetchFailed, "tree-hash mismatch in %s: got %s want %s", dir, tree, n.TreeHash)
	}
	return nil
}

// abbrev shortens a hash for display without assuming a minimum length.
func abbrev(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		return "", e.New(e.CodeUsage, "git execution requires a non-nil context")
	}
	cmd := execCmd(RunOptions{Ctx: ctx}, "git", append([]string{"-C", dir}, args...)...)
	out, err := outputOwnedCommand(ctx, cmd)
	if err != nil {
		if ctx.Err() != nil {
			return "", e.Wrap(e.CodeTimeout, ctx.Err(), "git execution canceled")
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
