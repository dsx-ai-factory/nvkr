// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// VerifyCheckout inspects a plain CI checkout through a private metadata view.
// Git owns HEAD, refs, objects, index and path syntax. No checkout-local config,
// including Runner's credential-bearing include.path, is loaded or modified.
// This admits inspection only, never source fetch or execution configuration.
// The borrowed object database is trusted: correspondence is with the runner's
// clone, without independent commit, tree or blob rehashing.
func VerifyCheckout(ctx context.Context, environment []string, dir, commit string, paths []string, maxEntries int) (returnErr error) {
	if ctx == nil || dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || maxEntries <= 0 {
		return e.New(e.CodeUsage, "checkout inspection requires a context, canonical root and positive index bound")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "checkout inspection canceled")
	}
	metadata := filepath.Join(dir, ".git")
	info, err := os.Lstat(metadata)
	if err != nil || !info.IsDir() {
		return e.New(e.CodeEngineCapability, "checkout inspection requires a plain Git metadata directory")
	}
	view, err := os.MkdirTemp("", "nvkr-checkout-inspection-")
	if err != nil {
		return e.Wrap(e.CodeUnavailable, err, "creating checkout inspection view")
	}
	defer func() {
		if err := os.RemoveAll(view); err != nil && returnErr == nil {
			returnErr = e.Wrap(e.CodeUnavailable, err, "closing checkout inspection view")
		}
	}()
	gitdir := filepath.Join(view, ".git")
	if err := os.Mkdir(gitdir, 0700); err != nil {
		return e.Wrap(e.CodeUnavailable, err, "creating checkout metadata view")
	}
	for _, item := range []struct {
		name                string
		directory, optional bool
	}{
		{"HEAD", false, false}, {"index", false, false}, {"objects", true, false}, {"refs", true, false}, {"packed-refs", false, true}, {"shallow", false, true},
	} {
		source := filepath.Join(metadata, item.name)
		info, err := os.Lstat(source)
		if item.optional && os.IsNotExist(err) {
			continue
		}
		if err != nil || (item.directory && !info.IsDir()) || (!item.directory && !info.Mode().IsRegular()) {
			return e.New(e.CodeEngineCapability, "checkout metadata member is missing or has an unsupported type")
		}
		if item.name == "HEAD" || item.name == "index" {
			// Git requires an ordinary HEAD file. Copy the index too so its
			// preliminary correspondence queries cannot refresh Runner metadata.
			// These are bounded exact bytes; Git owns both formats.
			limit := int64(64 << 20)
			if item.name == "HEAD" {
				limit = 64 << 10
			}
			file, err := os.Open(source)
			if err != nil {
				return e.Wrap(e.CodeProvenanceMissing, err, "opening checkout metadata snapshot")
			}
			raw, readErr := io.ReadAll(io.LimitReader(file, limit+1))
			closeErr := file.Close()
			if readErr != nil || closeErr != nil || int64(len(raw)) > limit {
				return e.New(e.CodeProvenanceMissing, "checkout metadata snapshot exceeds its bounded read")
			}
			if err := os.WriteFile(filepath.Join(gitdir, item.name), raw, 0600); err != nil {
				return e.Wrap(e.CodeUnavailable, err, "binding checkout metadata snapshot")
			}
		} else if err := os.Symlink(source, filepath.Join(gitdir, item.name)); err != nil {
			return e.Wrap(e.CodeUnavailable, err, "binding checkout metadata view")
		}
	}
	// Preserve LocalCommand's existing inert-empty .gitmodules exception in
	// the inspection view. Meaningful descriptors and gitlinks remain refused.
	if descriptor, err := os.Lstat(filepath.Join(dir, ".gitmodules")); err == nil {
		if !descriptor.Mode().IsRegular() || descriptor.Size() != 0 {
			return e.New(e.CodeEngineCapability, "checkout submodule descriptor is unsupported")
		}
		mode := os.FileMode(0600)
		if descriptor.Mode().Perm()&0111 != 0 {
			mode = 0700
		}
		if err := os.WriteFile(filepath.Join(view, ".gitmodules"), nil, mode); err != nil {
			return e.Wrap(e.CodeUnavailable, err, "binding checkout submodule descriptor")
		}
	} else if !os.IsNotExist(err) {
		return e.Wrap(e.CodeProvenanceMissing, err, "inspecting checkout submodule descriptor")
	}
	query := func(args ...string) ([]byte, error) {
		command, err := LocalCommand(ctx, environment, view, args...)
		if err != nil {
			return nil, err
		}
		// The owning adapter adds this one fixed global selector; caller paths stay
		// literal operands. Optional locks are disabled so inspection cannot refresh
		// the Runner's borrowed index.
		prefix := command.Args[1 : len(command.Args)-len(args)]
		selected := append(append([]string{}, prefix...), "--no-optional-locks", "-c", "core.commitGraph=false", "--work-tree="+dir)
		selected = append(selected, args...)
		return localGitQuery(ctx, command.Path, command.Env, selected, "checkout correspondence", 16<<20, false)
	}
	records, err := query("ls-files", "--cached", "-v", "-z")
	if err != nil {
		return err
	}
	if err := checkWorktreeIndexRecords(records, maxEntries); err != nil {
		return err
	}
	head, err := query("rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != commit {
		return e.New(e.CodeProvenanceMissing, "runner clone HEAD does not match the pipeline commit")
	}
	if _, err := query(append([]string{"ls-files", "--error-unmatch", "--"}, paths...)...); err != nil {
		return e.New(e.CodeProvenanceMissing, "checkout authored inputs are not tracked by the runner clone")
	}
	if _, err := query(append([]string{"diff", "--exit-code", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, paths...)...); err != nil {
		return e.New(e.CodeProvenanceMissing, "checkout input bytes differ from the runner clone")
	}
	// A clean Git diff can normalize CRLF or apply worktree conversions.
	// cat-file emits the committed blob without filters; compare the exact
	// regular-file bytes the caller consumes, retaining the existing Git checks.
	for _, relative := range paths {
		if !securepath.WorkspaceRelative(relative) || filepath.Clean(relative) != relative {
			return e.New(e.CodeProvenanceMissing, "checkout input path is not a canonical relative file")
		}
		blob, err := query("cat-file", "blob", commit+":"+filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		file, before, err := securepath.OpenRegular(filepath.Join(dir, relative))
		if err != nil {
			return e.New(e.CodeProvenanceMissing, "checkout input is not an available regular file")
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, int64(len(blob))+1))
		after, statErr := file.Stat()
		closeErr := file.Close()
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "checkout byte comparison canceled")
		}
		if readErr != nil || statErr != nil || closeErr != nil || !securepath.SameMetadata(before, after) || !bytes.Equal(blob, raw) {
			return e.New(e.CodeProvenanceMissing, "checkout raw input bytes differ from the runner clone")
		}
	}
	return nil
}

// CheckCheckoutCredentials refuses known credential bytes in the direct local
// config or Git's effective local/include configuration before a checkout is
// mounted into a build. It neither executes credential helpers nor scans other
// repository content. Configuration bytes and native diagnostics stay private.
func CheckCheckoutCredentials(ctx context.Context, environment []string, dir string, credentials ...string) error {
	if ctx == nil || dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return e.New(e.CodeUsage, "checkout credential inspection requires a context and canonical root")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "checkout credential inspection canceled")
	}
	for _, directory := range []string{dir, filepath.Join(dir, ".git")} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() {
			return e.New(e.CodeEngineCapability, "checkout credential inspection requires a plain Git metadata directory")
		}
	}
	searchPath, err := SearchPath(environment)
	if err != nil {
		return e.New(e.CodeEngineCapability, "checkout credential inspection requires an admitted Git search path")
	}
	baseline, err := Baseline(searchPath)
	if err != nil {
		return e.New(e.CodeEngineCapability, "checkout credential inspection requires an admitted Git search path")
	}
	git, err := executable(searchPath)
	if err != nil {
		return e.New(e.CodeEngineCapability, "checkout credential inspection requires an available Git executable")
	}
	file, before, err := securepath.OpenRegular(filepath.Join(dir, ".git", "config"))
	if err != nil {
		return e.New(e.CodeEngineCapability, "checkout credential configuration is not an available regular file")
	}
	const limit = 64 << 10
	raw, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "checkout credential inspection canceled")
	}
	if readErr != nil || statErr != nil || closeErr != nil || len(raw) > limit || !securepath.SameMetadata(before, after) {
		return e.New(e.CodeEngineCapability, "checkout credential configuration changed or exceeds its bounded read")
	}
	// Git, rather than a second config parser, owns includes and escapes. A
	// configuration query reads helper definitions as data without invoking them.
	effective, err := localConfigQuery(ctx, git, baseline, []string{"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false", "-C", dir, "config", "--local", "--includes", "--null", "--list"})
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return e.Wrap(e.CodeTimeout, canceled, "checkout credential inspection canceled")
		}
		var coded *e.Error
		if errors.As(err, &coded) && coded.Code == e.CodeTimeout {
			return e.New(e.CodeTimeout, "checkout credential inspection timed out")
		}
		return e.New(e.CodeEngineCapability, "checkout credential configuration could not be inspected within its bounds")
	}
	for _, credential := range credentials {
		if credential != "" && (bytes.Contains(raw, []byte(credential)) || bytes.Contains(effective, []byte(credential))) {
			return e.New(e.CodeEngineCapability, "checkout configuration contains a build-visible credential; enable credential-free Runner checkout and remove overriding settings")
		}
	}
	return nil
}
