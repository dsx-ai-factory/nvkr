// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"golang.org/x/sys/unix"
)

const locatorSuffix = ".ci-locator.json"

// GitHubUpload selects exact workspace paths for one immutable provider index.
// Credentials are explicit and belong only to the transfer step.
type GitHubUpload struct {
	Request            Request
	Options            Options
	Root               string
	Paths              string
	OutputDirectory    string
	IfNoFilesFound     string
	IncludeHiddenFiles bool
}

// ValidateGitHubTransfer checks explicit context, identity and credential syntax
// without filesystem or network work. Registry privacy is still admitted by the
// transfer owner; this check issues no capability or acceptance receipt.
func ValidateGitHubTransfer(ctx context.Context, request Request, options Options) error {
	_, cancel, _, err := admit(ctx, request, options)
	if err != nil {
		return err
	}
	cancel()
	return nil
}

// UploadGitHub writes only a small locator for the provider's artifact action.
// There is no public-payload fallback when private storage refuses admission.
func UploadGitHub(ctx context.Context, input GitHubUpload) (string, error) {
	return uploadGitHub(ctx, input, Upload)
}

func uploadGitHub(ctx context.Context, input GitHubUpload, upload func(context.Context, Request, string, []string, Options) (Locator, error)) (string, error) {
	ctx, cancel, options, err := admit(ctx, input.Request, input.Options)
	if err != nil {
		return "", err
	}
	defer cancel()
	if input.Root == "" || input.OutputDirectory == "" || input.Request.Name == "" ||
		filepath.Base(input.Request.Name) != input.Request.Name || strings.ContainsAny(input.Request.Name, "\\\r\n") ||
		strings.ContainsAny(input.OutputDirectory, "\r\n\x00") {
		return "", e.New(e.CodeUsage, "private CI upload requires an exact artifact name, workspace and locator destination")
	}
	switch input.IfNoFilesFound {
	case "error", "warn", "ignore":
	default:
		return "", e.New(e.CodeUsage, "private CI upload requires a supported missing-file policy")
	}
	// Generated actions supply a fresh mktemp directory. Hold that directory
	// before payload work; never create or follow a substituted output directory.
	directory, directoryInfo, err := securepath.OpenDirectory(input.OutputDirectory)
	if err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "holding private CI locator directory")
	}
	defer func() { _ = directory.Close() }()
	name := input.Request.Name + locatorSuffix
	var existing unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return "", e.New(e.CodeImmutableViolation, "private CI locator already exists")
	} else if !errors.Is(err, unix.ENOENT) {
		return "", e.Wrap(e.CodeImmutableViolation, err, "checking private CI locator destination")
	}
	root, err := filepath.Abs(input.Root)
	if err != nil {
		return "", e.Wrap(e.CodeUsage, err, "resolve CI upload workspace")
	}
	var paths []string
	for _, line := range strings.Split(input.Paths, "\n") {
		if err := ctx.Err(); err != nil {
			return "", e.Wrap(e.CodeTimeout, err, "private CI path selection canceled")
		}
		value := strings.TrimSpace(line)
		if value == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(root, value)
		}
		relative, err := filepath.Rel(root, value)
		if err != nil || !filepath.IsLocal(relative) || relative == "." {
			return "", e.New(e.CodeUsage, "private CI upload paths must be exact children of the workspace")
		}
		info, err := os.Lstat(value)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", e.Wrap(e.CodeReleaseIncomplete, err, "inspect private CI upload member")
		}
		if info.IsDir() && !input.IncludeHiddenFiles {
			return "", e.New(e.CodeEngineCapability, "directory CI uploads require explicit hidden-file selection")
		}
		paths = append(paths, filepath.ToSlash(relative))
	}
	if len(paths) == 0 {
		if input.IfNoFilesFound == "error" {
			return "", e.New(e.CodeReleaseIncomplete, "private CI upload selected no existing files")
		}
		return "", nil
	}
	locator, err := upload(ctx, input.Request, root, paths, options)
	if err != nil {
		return "", err
	}
	if err := locator.Validate(input.Request); err != nil {
		return "", err
	}
	raw, err := canonical.Marshal(locator)
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "encode private CI locator")
	}
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "private CI locator output canceled")
	}
	if err := requireGitHubLocatorDirectory(input.OutputDirectory, directoryInfo); err != nil {
		return "", err
	}
	file, temporary, err := securepath.CreateTemporarySibling(directory)
	if err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "create private CI locator")
	}
	defer func() { _ = unix.Unlinkat(int(directory.Fd()), temporary, 0) }()
	_, writeErr := file.Write(raw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return "", e.Wrap(e.CodeInternal, writeErr, "write private CI locator")
	}
	if closeErr != nil {
		return "", e.Wrap(e.CodeInternal, closeErr, "close private CI locator")
	}
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "private CI locator publication canceled")
	}
	if err := transportarchive.PublishAtNoReplace(int(directory.Fd()), temporary, int(directory.Fd()), name); err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "publish private CI locator without replacement")
	}
	if err := directory.Sync(); err != nil {
		_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		return "", e.Wrap(e.CodeImmutableViolation, err, "persist private CI locator directory")
	}
	if err := requireGitHubLocatorDirectory(input.OutputDirectory, directoryInfo); err != nil {
		_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		return "", err
	}
	return filepath.Join(input.OutputDirectory, name), nil
}

func requireGitHubLocatorDirectory(path string, expected os.FileInfo) error {
	current, info, err := securepath.OpenDirectory(path)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "reopening private CI locator directory")
	}
	_ = current.Close()
	if !os.SameFile(expected, info) {
		return e.New(e.CodeImmutableViolation, "private CI locator directory changed during upload")
	}
	return nil
}

// DownloadGitHub resolves only uniquely named locator records from a fresh
// provider download. Provider-created artifact directories retain the original
// download action's merge policy. The owning store authenticates every request.
func DownloadGitHub(ctx context.Context, request Request, index, destination string, options Options) error {
	return newBackend().downloadGitHub(ctx, request, index, destination, options)
}

func (b *backend) downloadGitHub(ctx context.Context, request Request, index, destination string, options Options) error {
	if ctx == nil || index == "" || destination == "" {
		return e.New(e.CodeUsage, "private CI download requires context, index and destination")
	}
	// The index may select several artifact names. Admit its actual repository
	// and attempt before opening it; each filename is independently validated
	// against its locator and the same request before Download.
	admission := request
	if admission.Name == "" {
		admission.Name = "private-ci-index"
	}
	if err := ValidateGitHubRequest(ctx, admission); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	totalBytes := 0
	seen := map[string]bool{}
	count := 0
	err := filepath.WalkDir(index, func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "private CI index traversal canceled")
		}
		if walkErr != nil {
			return e.Wrap(e.CodeFetchFailed, walkErr, "read private CI index")
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type() != 0 || !strings.HasSuffix(entry.Name(), locatorSuffix) {
			return e.New(e.CodeVerifyFailed, "CI index contains a non-locator member")
		}
		file, err := heldfile.OpenContext(ctx, name, heldfile.Options{Label: "private CI locator", MaxBytes: MaxMetadataRecordBytes})
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "open private CI locator")
		}
		raw, readErr := file.ReadAllContext(ctx, MaxMetadataRecordBytes)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return e.New(e.CodeFetchFailed, "read private CI locator failed")
		}
		totalBytes += len(raw)
		if totalBytes > maxGitHubIndexBytes || count >= MaxGitHubLocatorArtifacts {
			return e.New(e.CodeVerifyFailed, "CI index exceeds its aggregate bound")
		}
		locator, err := DecodeLocator(raw)
		if err != nil {
			return err
		}
		selected := request
		selected.Name = strings.TrimSuffix(entry.Name(), locatorSuffix)
		if request.Name != "" && request.Name != selected.Name {
			return e.New(e.CodeVerifyFailed, "CI index name differs from the selected artifact")
		}
		if seen[selected.Name] {
			return e.New(e.CodeVerifyFailed, "CI index repeats a selected artifact")
		}
		seen[selected.Name] = true
		if locator.Metadata == nil {
			if err := ValidateGitHubTransfer(ctx, selected, options); err != nil {
				return err
			}
		}
		parent, err := filepath.Rel(index, filepath.Dir(name))
		if err != nil || !filepath.IsLocal(parent) {
			return e.New(e.CodeVerifyFailed, "CI locator parent escapes its provider index")
		}
		if err := b.download(ctx, selected, locator, filepath.Join(destination, parent), options); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return e.New(e.CodeReleaseIncomplete, "CI index contains no private artifact locators")
	}
	return nil
}

// WriteLocatorOutput exposes a path, never artifact bytes or storage credentials.
func WriteLocatorOutput(writer io.Writer, path string) error {
	if writer == nil || strings.ContainsAny(path, "\r\n") {
		return e.New(e.CodeUsage, "CI locator output requires a writer and a single-line path")
	}
	if _, err := fmt.Fprintf(writer, "locator_path=%s\n", path); err != nil {
		return e.Wrap(e.CodeInternal, err, "write CI locator step output")
	}
	return nil
}
