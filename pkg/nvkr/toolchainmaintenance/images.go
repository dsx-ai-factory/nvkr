// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

const maxArchiveBytes int64 = 8 << 30

type measurement struct {
	SchemaVersion          string            `json:"schemaVersion"`
	Scope                  string            `json:"scope"`
	SourceCommit           string            `json:"sourceCommit"`
	Platform               string            `json:"platform"`
	PackerImageID          string            `json:"packerImageID"`
	InspectorImageID       string            `json:"inspectorImageID"`
	Archives               map[string]string `json:"archives"`
	ExecutableMeasurements string            `json:"executableMeasurements"`
	PublicationApproved    bool              `json:"publicationApproved"`
}

type heldImage struct {
	file        *os.File
	image       v1.Image
	description Image
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// snapshotArchive owns an unlinked disk-backed snapshot. Toolchain archives are
// too large for heldfile's sealed-memory representation. The native hosted lane
// trusts its runner; the snapshot prevents pathname replacement during upload.
func snapshotArchive(ctx context.Context, path string) (_ *os.File, digest string, err error) {
	if err = ctx.Err(); err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxArchiveBytes {
		return nil, "", fmt.Errorf("archive must be regular and at most %d bytes", maxArchiveBytes)
	}
	source, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, "", fmt.Errorf("archive replaced while opening")
	}
	copyFile, err := os.CreateTemp("", "nvkr-toolchain-*")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = copyFile.Close(); _ = os.Remove(copyFile.Name()) }()
	hash := sha256.New() // Exact archive bytes, not a structured identity.
	n, err := io.Copy(io.MultiWriter(copyFile, hash), io.LimitReader(contextReader{ctx, source}, maxArchiveBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("archive copy incomplete: %w", err)
	}
	if n != info.Size() {
		return nil, "", fmt.Errorf("archive size changed while copying")
	}
	if err := copyFile.Chmod(0400); err != nil {
		return nil, "", err
	}
	reader, err := os.Open(copyFile.Name())
	if err != nil {
		return nil, "", err
	}
	return reader, fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

func openImage(ctx context.Context, directory, arch, kind, source string) (_ *heldImage, err error) {
	raw, err := read(ctx, filepath.Join(directory, "measurement.json"))
	if err != nil {
		return nil, err
	}
	var m measurement
	if err := decode(raw, &m); err != nil {
		return nil, err
	}
	if m.SchemaVersion != "nvkr.dev/native-toolchain-measurement/v2" || m.Scope != "unqualified-diagnostic" || m.SourceCommit != source || m.Platform != "linux/"+arch || m.PublicationApproved || len(m.Archives) != 2 {
		return nil, e.New(e.CodeVerifyFailed, "native measurement does not bind the requested source/platform")
	}
	f, archiveDigest, err := snapshotArchive(ctx, filepath.Join(directory, kind+".tar"))
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "snapshot native archive")
	}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	if archiveDigest != m.Archives[kind] {
		return nil, e.New(e.CodeVerifyFailed, "archive differs from native measurement")
	}
	info, err := f.Stat()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "stat held archive")
	}
	im, err := tarball.Image(func() (io.ReadCloser, error) {
		return io.NopCloser(contextReader{ctx, io.NewSectionReader(f, 0, info.Size())}), nil
	}, nil)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "read single native image")
	}
	config, err := im.ConfigFile()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "read native image configuration")
	}
	id, err := im.ConfigName()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "measure image configuration")
	}
	expectedID := m.PackerImageID
	if kind == "inspector" {
		expectedID = m.InspectorImageID
	}
	if config.OS != "linux" || config.Architecture != arch || id.String() != expectedID {
		return nil, e.New(e.CodeVerifyFailed, "image bytes disagree with native measurement")
	}
	digest, err := im.Digest()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "measure native manifest")
	}
	measurementHash := sha256.Sum256(raw)
	description := Image{Arch: arch, Kind: kind, ArchiveDigest: archiveDigest, ImageID: id.String(), ManifestDigest: digest.String(), MeasurementDigest: fmt.Sprintf("sha256:%x", measurementHash), Exception: Exception{Scope: Scope}}
	if kind == "inspector" {
		description.Executables, err = parseExecutables(m.ExecutableMeasurements)
		if err != nil {
			return nil, err
		}
	}
	return &heldImage{file: f, image: im, description: description}, nil
}

func parseExecutables(measured string) (map[string]string, error) {
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(measured), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, e.New(e.CodeVerifyFailed, "invalid executable measurement")
		}
		found := false
		for key, path := range executablePaths {
			if fields[1] == path {
				if _, exists := result[key]; exists {
					return nil, e.New(e.CodeVerifyFailed, "duplicate executable measurement")
				}
				result[key] = "sha256:" + fields[0]
				if !canonical.IsDigest(result[key]) {
					return nil, e.New(e.CodeVerifyFailed, "invalid executable digest")
				}
				found = true
			}
		}
		if !found {
			return nil, e.New(e.CodeVerifyFailed, "unknown executable measurement")
		}
	}
	if len(result) != len(executablePaths) {
		return nil, e.New(e.CodeVerifyFailed, "incomplete executable measurement")
	}
	return result, nil
}

// Describe emits a deliberately unapproved per-architecture proposal. A maintainer
// combines both proposals and records independent license/security review in main.
func Describe(ctx context.Context, directory, arch, source string) ([]Image, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "native description requires context")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if (arch != "amd64" && arch != "arm64") || !commitPattern.MatchString(source) {
		return nil, e.New(e.CodeUsage, "describe requires native arch and exact source commit")
	}
	var result []Image
	for _, kind := range []string{"inspector", "packer"} {
		image, err := openImage(ctx, directory, arch, kind, source)
		if err != nil {
			return nil, err
		}
		result = append(result, image.description)
		_ = image.file.Close()
	}
	return result, nil
}

func sameSubject(a, b Image) bool {
	return a.Key() == b.Key() && a.ArchiveDigest == b.ArchiveDigest && a.ImageID == b.ImageID && a.ManifestDigest == b.ManifestDigest && a.MeasurementDigest == b.MeasurementDigest && maps.Equal(a.Executables, b.Executables)
}

func openApprovedImages(ctx context.Context, directory string, r Record) (_ []*heldImage, err error) {
	var images []*heldImage
	defer func() {
		if err != nil {
			closeImages(images)
		}
	}()
	for _, expected := range r.Images {
		im, err := openImage(ctx, filepath.Join(directory, expected.Arch), expected.Arch, expected.Kind, r.SourceCommit)
		if err != nil {
			return nil, err
		}
		images = append(images, im)
		if !sameSubject(im.description, expected) {
			return nil, e.New(e.CodeVerifyFailed, "approved subject differs from retained %s bytes", expected.Key())
		}
	}
	return images, nil
}

func closeImages(images []*heldImage) {
	for _, im := range images {
		_ = im.file.Close()
	}
}

func inspectorFields() []string { return slices.Sorted(maps.Keys(executablePaths)) }
