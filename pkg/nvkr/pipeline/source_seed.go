// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"golang.org/x/sys/unix"
)

// SourceSeed is an untrusted description of a same-Run checkout and its fetch
// instruction. The fetch owner authenticates both it and the consumer; this
// Platform describes the seed owner's Plan-selected native platform. This
// description is not a source receipt or permission to skip consumer admission.
type SourceSeed struct {
	Instruction ir.Node
	Root        string
	Platform    string
}

// seedSourceWorkspace restores a same-Run source into a consumer's private
// checkout. The descriptions grant no authority: authenticate the seed's pinned
// Git revision and closure, then verify the copied bytes. Matching vendor
// specifications and native platforms reuse a closure; other consumers receive only Git metadata and
// reconstruct the pristine pinned tree before their ordinary fetch/vendors run.
// The subsequent fetch still admits the consumer and rehashes its own checkout.
func seedSourceWorkspace(ctx context.Context, root *os.File, seed ir.Node, seedRoot string, consumer ir.Node, consumerRoot string, seedPlatform string, environment []string) (returnErr error) {
	defer func() { returnErr = mapCancellation(ctx, returnErr, "source seed restoration") }()
	if seed.Kind != ir.KindFetch || consumer.Kind != ir.KindFetch || seed.Repo == "" || seed.Repo != consumer.Repo ||
		seed.Commit != consumer.Commit || seed.TreeHash != consumer.TreeHash {
		return e.New(e.CodeFetchFailed, "shared source does not match the consumer's repository, commit and tree")
	}
	if seedPlatform != "linux/amd64" && seedPlatform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "shared source requires an admitted native seed platform")
	}
	seedPath, consumerPath := filepath.Join(seedRoot, seed.Dest), filepath.Join(consumerRoot, consumer.Dest)
	if !securepath.WorkspaceRelative(seedRoot) || !securepath.WorkspaceRelative(consumerRoot) ||
		!securepath.WorkspaceRelative(seed.Dest) || !securepath.WorkspaceRelative(consumer.Dest) ||
		filepath.Clean(seedRoot) != seedRoot || filepath.Clean(consumerRoot) != consumerRoot ||
		seedPath == consumerPath || strings.HasPrefix(seedPath, consumerPath+"/") || strings.HasPrefix(consumerPath, seedPath+"/") {
		return e.New(e.CodeEngineCapability, "source seed requires distinct workspace-relative checkouts")
	}
	seedVendor, err := fetchVendorSpecDigest(seed)
	if err != nil {
		return err
	}
	consumerVendor, err := fetchVendorSpecDigest(consumer)
	if err != nil {
		return err
	}
	reuseClosure := seedVendor == consumerVendor && seedPlatform == "linux/"+runtime.GOARCH
	// Walk existing components without creating a missing seed path.
	seedDirectory, err := openSourceSeedDirectory(ctx, root, seedPath)
	if err != nil {
		return err
	}
	defer seedDirectory.Close()
	seedReceiptPath, err := sourceReceiptPath(seedRoot, seed.Dest)
	if err != nil {
		return err
	}
	seedReceipts, err := openSourceSeedDirectory(ctx, root, filepath.Dir(seedReceiptPath))
	if err != nil {
		return err
	}
	defer seedReceipts.Close()
	receipt, exists, err := readSourceReceiptContext(ctx, seedReceiptPath, seedReceipts)
	if err != nil {
		return err
	}
	if !exists {
		return e.New(e.CodeFetchFailed, "shared source has no receipt")
	}
	seedDescriptor, err := sourceFetchDescriptorPath(seedDirectory.Fd())
	if err != nil {
		return err
	}
	if err := verifyRestoredClosure(ctx, seed, seedDescriptor, seedVendor, receipt); err != nil {
		return err
	}
	seedGit, _, err := securepath.OpenChild(seedDirectory, ".git", true)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "opening shared source Git metadata")
	}
	defer seedGit.Close()
	seedOptions := RunOptions{Ctx: ctx, SourceRoot: seedRoot, fetchWorkspace: root, fetchDirectory: seedDirectory,
		fetchGitDirectory: seedGit, fetchReceiptDirectory: seedReceipts, fetchRepository: seed.Repo, SourceGitEnvironment: environment}
	if err := recheckSourceFetchDirectories(seed, seedOptions); err != nil {
		return err
	}
	consumerDirectory, err := holdSourceFetchWorkspaceDirectory(ctx, root, consumerPath)
	if err != nil {
		return err
	}
	defer consumerDirectory.Close()
	names, err := sourceChildNames(consumerDirectory)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "checking fresh source consumer")
	}
	if len(names) != 0 {
		return e.New(e.CodeFetchFailed, "source seed destination is not empty")
	}
	stamp, entries := time.Unix(0, 0).UTC(), 0
	if reuseClosure {
		if err := copySourceDirectoryContext(ctx, seedDirectory, consumerDirectory, "", stamp, &entries); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "copying shared source closure")
		}
	}
	if err := unix.Mkdirat(int(consumerDirectory.Fd()), ".git", 0700); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating source consumer Git directory")
	}
	consumerGit, _, err := securepath.OpenChild(consumerDirectory, ".git", true)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "holding source consumer Git directory")
	}
	defer consumerGit.Close()
	opts := RunOptions{Ctx: ctx, SourceRoot: consumerRoot, fetchWorkspace: root, fetchDirectory: consumerDirectory,
		fetchGitDirectory: consumerGit, fetchRepository: consumer.Repo, SourceGitEnvironment: environment}
	if err := restoreSeedGitObjects(ctx, seedGit, consumerGit, consumerPath, consumer.Commit, opts); err != nil {
		return err
	}
	if !reuseClosure {
		if _, err := sourceFetchGitOut(opts, consumerPath, "checkout", "--force", "--detach", consumer.Commit); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "reconstructing consumer's pristine pinned source")
		}
		if err := (Local{}).verifyTree(consumerPath, consumer, opts); err != nil {
			return err
		}
		return recheckSourceFetchDirectory(ctx, root, consumerPath, consumerDirectory)
	}
	// Populate only the index and detached HEAD: checking out would overwrite
	// the exact verified vendored closure copied above.
	for _, args := range [][]string{{"read-tree", consumer.Commit}, {"update-ref", "--no-deref", "HEAD", consumer.Commit}} {
		if _, err := sourceFetchGitOut(opts, consumerPath, args...); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "binding shared source Git revision")
		}
	}
	path, err := sourceFetchDescriptorPath(consumerDirectory.Fd())
	if err != nil {
		return err
	}
	if err := verifyRestoredClosure(ctx, consumer, path, consumerVendor, receipt); err != nil {
		return err
	}
	if err := (Local{}).verifyTree(consumerPath, consumer, opts); err != nil {
		return err
	}
	consumerReceiptPath, err := sourceReceiptPath(consumerRoot, consumer.Dest)
	if err != nil {
		return err
	}
	receipts, err := holdSourceFetchWorkspaceDirectory(ctx, root, filepath.Dir(consumerReceiptPath))
	if err != nil {
		return err
	}
	defer receipts.Close()
	opts.fetchReceiptDirectory = receipts
	if err := recheckSourceFetchDirectories(consumer, opts); err != nil {
		return err
	}
	return writeSourceReceiptContext(ctx, consumerReceiptPath, receipt, receipts)
}

func openSourceSeedDirectory(ctx context.Context, root *os.File, path string) (*os.File, error) {
	current, owned := root, false
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if err := ctx.Err(); err != nil {
			if owned {
				_ = current.Close()
			}
			return nil, e.Wrap(e.CodeTimeout, err, "opening shared source directory canceled")
		}
		next, _, err := securepath.OpenChild(current, component, true)
		if owned {
			_ = current.Close()
		}
		if err != nil {
			return nil, e.Wrap(e.CodeFetchFailed, err, "opening shared source directory")
		}
		current, owned = next, true
	}
	return current, nil
}

// Import only the object store and shallow boundary into a fresh repository.
// Git owns object parsing and recomputes integrity with fsck before checkout.
// Config, attributes, refs, grafts, hooks and index state never cross this boundary.
func restoreSeedGitObjects(ctx context.Context, seedGit, consumerGit *os.File, path, commit string, opts RunOptions) error {
	objects, _, err := securepath.OpenChild(seedGit, "objects", true)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "opening shared source Git objects")
	}
	defer objects.Close()
	info, _, err := securepath.OpenChild(objects, "info", true)
	if err == nil {
		names, readErr := sourceChildNames(info)
		_ = info.Close()
		if readErr != nil || len(names) != 0 {
			return e.Wrap(e.CodeFetchFailed, readErr, "shared Git objects contain unsupported info or alternate object sources")
		}
	} else if !os.IsNotExist(err) {
		return e.Wrap(e.CodeFetchFailed, err, "opening shared Git object info")
	}
	if err := unix.Mkdirat(int(consumerGit.Fd()), "objects", 0700); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating private Git object store")
	}
	copied, _, err := securepath.OpenChild(consumerGit, "objects", true)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "holding private Git object store")
	}
	defer copied.Close()
	entries := 0
	if err := copySourceDirectoryContext(ctx, objects, copied, ".git/objects", time.Unix(0, 0).UTC(), &entries); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "copying shared Git objects")
	}
	entries = 0
	if err := admitSeedGitObjects(ctx, copied, "", &entries); err != nil {
		return err
	}
	shallow, err := securepath.InspectChild(seedGit, "shallow", ".git/shallow")
	if err == nil {
		if shallow.Kind != "file" {
			if shallow.File != nil {
				_ = shallow.File.Close()
			}
			return e.New(e.CodeFetchFailed, "shared Git shallow boundary is not a regular file")
		}
		defer shallow.File.Close()
		if err := copyOpenSourceFileContext(ctx, shallow, consumerGit, "shallow", ".git/shallow", time.Unix(0, 0).UTC()); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "copying shared Git shallow boundary")
		}
	} else if !os.IsNotExist(err) {
		return e.Wrap(e.CodeFetchFailed, err, "opening shared Git shallow boundary")
	}
	for _, args := range [][]string{{"init", "-q", "--template="}, {"fsck", "--full", "--no-reflogs", "--no-dangling", commit}} {
		if _, err := sourceFetchGitOut(opts, path, args...); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "authenticating shared source Git objects")
		}
	}
	return nil
}

// Validate the private copy before invoking Git. Source-side checks cannot
// exclude links or alternate metadata introduced while that source is copied.
func admitSeedGitObjects(ctx context.Context, directory *os.File, prefix string, entries *int) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "checking private Git object store canceled")
	}
	names, err := sourceChildNames(directory)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "reading private Git object store")
	}
	if prefix == "info" && len(names) != 0 {
		return e.New(e.CodeFetchFailed, "private Git object info must be empty")
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "checking private Git object store canceled")
		}
		*entries++
		if *entries > maxSourceClosureEntries {
			return e.New(e.CodeFetchFailed, "private Git object store exceeds its entry bound")
		}
		relative := filepath.Join(prefix, name)
		entry, err := securepath.InspectChild(directory, name, relative)
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "checking private Git object entry")
		}
		if entry.Kind != "file" && entry.Kind != "directory" {
			if entry.File != nil {
				_ = entry.File.Close()
			}
			return e.New(e.CodeFetchFailed, "private Git objects require only regular files and directories")
		}
		if entry.Kind == "directory" {
			err = admitSeedGitObjects(ctx, entry.File, relative, entries)
		}
		closeErr := entry.File.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return e.Wrap(e.CodeFetchFailed, closeErr, "closing private Git object entry")
		}
	}
	return nil
}
