// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// All destination work uses held directory descriptors. populate must finish
// verification and sync its files before returning; only then can out appear.
func publishPullDirectory(ctx context.Context, out string, populate func(*os.File) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	out = filepath.Clean(out)
	parent, _, err := securepath.OpenDirectory(filepath.Dir(out))
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening output destination parent (it must already exist)")
	}
	defer parent.Close()
	stage, temporary, err := createImageIndexStage(parent)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating private output destination")
	}
	defer stage.Close()
	committed := false
	defer func() {
		if !committed {
			removePullChildren(stage)
			_ = unix.Unlinkat(int(parent.Fd()), temporary, unix.AT_REMOVEDIR)
		}
	}()
	if err := populate(stage); err != nil {
		return classifyContext(ctx, err)
	}
	if err := syncPullDirectories(ctx, stage); err != nil {
		return err
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), filepath.Base(out)); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing output directory without replacement")
	}
	committed = true
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persisting output directory commit")
	}
	return nil
}

func syncPullDirectories(ctx context.Context, directory *os.File) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "rewinding private output directory")
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "reading private output directory")
	}
	for _, entry := range entries {
		if entry.IsDir() {
			child, _, err := securepath.OpenChild(directory, entry.Name(), true)
			if err != nil {
				return e.Wrap(e.CodeImmutableViolation, err, "opening private output directory")
			}
			err = syncPullDirectories(ctx, child)
			_ = child.Close()
			if err != nil {
				return err
			}
		} else if !entry.Type().IsRegular() {
			return e.New(e.CodeVerifyFailed, "output contains a non-regular member %q", entry.Name())
		}
	}
	if err := directory.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persisting output directory")
	}
	return nil
}

// Cleanup intentionally runs after cancellation and never follows symlinks.
func removePullChildren(directory *os.File) {
	// APT directories may preserve read-only modes. This is an owned, private
	// inode, so restore cleanup permissions without resolving a caller pathname.
	_ = directory.Chmod(0o700)
	_, _ = directory.Seek(0, io.SeekStart)
	entries, _ := directory.ReadDir(-1)
	for _, entry := range entries {
		if entry.IsDir() {
			child, _, err := securepath.OpenChild(directory, entry.Name(), true)
			if err == nil {
				removePullChildren(child)
				_ = child.Close()
			}
			_ = unix.Unlinkat(int(directory.Fd()), entry.Name(), unix.AT_REMOVEDIR)
		} else {
			_ = unix.Unlinkat(int(directory.Fd()), entry.Name(), 0)
		}
	}
}

func pullDiskOutput(ctx context.Context, reference, out, metadataRoot, workspace string, object PublishedObject, options RegistryOptions) error {
	if object.Output == nil || object.Output.Disk == nil {
		return e.New(e.CodeVerifyFailed, "disk output has no exact payload binding")
	}
	binding := object.Output.Disk
	manifest, platform, err := inspectDiskMetadata(ctx, metadataRoot,
		Descriptor{MediaType: MediaOCIManifest, ArtifactType: object.ArtifactType, Digest: object.DescriptorDigest, Size: object.Size},
		object.OS+"/"+object.Arch, maxDeliveryFileBytes)
	if err != nil {
		return err
	}
	layer := manifest.Layers[0]
	if binding.PayloadDigest != layer.Digest.String() || binding.Size != layer.Size || object.BlobDigest != binding.PayloadDigest ||
		!supportedDiskEncryption(binding.Encryption) || manifest.Annotations[diskEncryptionAnnotation] != binding.Encryption ||
		platform.OS != object.OS || platform.Architecture != object.Arch {
		return e.New(e.CodeVerifyFailed, "disk payload differs from its exact release binding")
	}
	source, err := fetchPullLayer(ctx, reference, Descriptor{MediaType: layer.MediaType, Digest: layer.Digest.String(), Size: layer.Size}, workspace, options)
	if err != nil {
		return err
	}
	defer source.Close()
	return publishPullDirectory(ctx, out, func(stage *os.File) error {
		return writeImageIndexFile(ctx, stage, object.Name+".qcow2", func(destination io.Writer) error {
			_, err := streamDiskPayload(ctx, source, destination, binding.PayloadDigest, binding.Size)
			return err
		})
	})
}
