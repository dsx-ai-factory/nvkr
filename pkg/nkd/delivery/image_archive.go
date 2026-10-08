// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// ImportContainerImage captures an observed OCI-export tar and verifies its
// runnable image closure before publishing a layout. ArchiveDigest binds exact
// producer bytes; the returned manifest digest is the distinct pullable image
// identity. This operation grants no execution or release authority.
func ImportContainerImage(ctx context.Context, archive, archiveDigest, platform, out string) (ImageInspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return ImageInspection{}, err
	}
	if !isDigest(archiveDigest) || strings.TrimSpace(out) == "" {
		return ImageInspection{}, e.New(e.CodeUsage, "container image import requires an exact archive digest and output path")
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return ImageInspection{}, e.New(e.CodeEngineCapability, "container image platform %q is unsupported", platform)
	}
	held, err := heldfile.OpenContext(ctx, archive, heldfile.Options{Label: "OCI image export", MaxBytes: maxDeliveryFileBytes})
	if err != nil {
		return ImageInspection{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing OCI image export"))
	}
	defer func() { _ = held.Close() }()
	if held.Digest() != archiveDigest {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "OCI image export differs from observed archive digest")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return ImageInspection{}, err
		}
		return ImageInspection{}, e.Wrap(e.CodeInternal, err, "staging OCI image export")
	}
	defer cleanup()
	if err := extractImageArchive(ctx, io.NewSectionReader(held, 0, held.Size()), stage); err != nil {
		return ImageInspection{}, err
	}
	image, err := InspectContainerImage(ctx, stage, platform)
	if err != nil {
		return ImageInspection{}, err
	}
	if err := deliveryContext(ctx); err != nil {
		return ImageInspection{}, err
	}
	if err := commit(); err != nil {
		return ImageInspection{}, err
	}
	return image, nil
}

func extractImageArchive(ctx context.Context, source io.Reader, root string) error {
	directory, err := os.OpenRoot(root)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening OCI image extraction root")
	}
	defer func() { _ = directory.Close() }()
	reader := tar.NewReader(source)
	seen := map[string]bool{}
	var total int64
	for {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading OCI image archive")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(header.Name, "./"), "/")
		if !filepath.IsLocal(name) || name != path.Clean(name) || strings.Contains(name, "\\") || seen[name] ||
			len(seen) >= maxDeliveryObjects*8 || header.Size < 0 || header.Size > maxDeliveryFileBytes-total {
			return e.New(e.CodeVerifyFailed, "OCI image archive contains an unsafe, duplicate or oversized entry")
		}
		seen[name] = true
		total += header.Size
		if header.Typeflag == tar.TypeDir {
			if (name != "blobs" && name != "blobs/sha256") || header.Size != 0 {
				return e.New(e.CodeVerifyFailed, "OCI image archive has unexpected directory %q", name)
			}
			continue
		}
		blob := strings.TrimPrefix(name, "blobs/sha256/")
		if header.Typeflag != tar.TypeReg || (name != "index.json" && name != "oci-layout" &&
			(!strings.HasPrefix(name, "blobs/sha256/") || !isDigest("sha256:"+blob))) {
			return e.New(e.CodeVerifyFailed, "OCI image archive has unexpected member %q", name)
		}
		if err := writeImageArchiveMember(ctx, reader, directory, filepath.FromSlash(name)); err != nil {
			return err
		}
	}
	if !seen["oci-layout"] || !seen["index.json"] {
		return e.New(e.CodeVerifyFailed, "OCI image archive omits layout metadata")
	}
	return nil
}

func writeImageArchiveMember(ctx context.Context, source io.Reader, root *os.Root, destination string) error {
	if err := root.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "creating OCI image blob directory")
	}
	file, err := root.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "creating OCI image member")
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, 128<<10)
	for {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		n, readErr := source.Read(buffer)
		if _, err := file.Write(buffer[:n]); err != nil {
			return e.Wrap(e.CodeInternal, err, "writing OCI image member")
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return e.Wrap(e.CodeVerifyFailed, readErr, "reading OCI image member")
		}
	}
	if err := file.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "syncing OCI image member")
	}
	return nil
}

func verifyImageLayoutMarker(ctx context.Context, root string) error {
	held, err := heldfile.OpenContext(ctx, filepath.Join(root, "oci-layout"), heldfile.Options{Label: "OCI layout marker", MaxBytes: 4096})
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding OCI layout marker"))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, 4096)
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading OCI layout marker"))
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "validating OCI layout marker JSON")
	}
	var marker struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil || marker.Version != "1.0.0" {
		return e.New(e.CodeVerifyFailed, "OCI image layout version is unsupported")
	}
	return nil
}
