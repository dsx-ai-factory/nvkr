// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/helmarchive"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func pullHelmOutput(ctx context.Context, reference, out, metadataRoot, workspace string, object PublishedObject, options RegistryOptions) (PulledFile, error) {
	manifest, err := flatManifest(ctx, metadataRoot, object)
	if err != nil {
		return PulledFile{}, err
	}
	if object.Output == nil || object.Output.Format != outputFormatHelm || object.Output.TransportPath != "" ||
		manifest.ArtifactType != object.ArtifactType || manifest.Config.MediaType != MediaHelmConfig || len(manifest.Layers) != 1 ||
		manifest.Layers[0].MediaType != MediaHelmChartLayer || manifest.Layers[0].Digest != object.BlobDigest ||
		manifest.Layers[0].Size < 0 || manifest.Layers[0].Size > helmarchive.MaxPackageBytes {
		return PulledFile{}, e.New(e.CodeVerifyFailed, "selected Helm output has invalid media, payload or transport binding")
	}
	config, err := readHelmChartConfig(ctx, metadataRoot, manifest)
	if err != nil {
		return PulledFile{}, err
	}
	file, err := fetchPullLayer(ctx, reference, manifest.Layers[0], workspace, options)
	if err != nil {
		return PulledFile{}, err
	}
	defer func() { _ = file.Close() }()
	var archive bytes.Buffer
	if _, err := streamDiskPayload(ctx, file, &archive, manifest.Layers[0].Digest, manifest.Layers[0].Size); err != nil {
		return PulledFile{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading selected Helm archive"))
	}
	identity, err := helmarchive.Inspect(ctx, archive.Bytes())
	if err != nil {
		return PulledFile{}, err
	}
	if identity.Name != config.Name || identity.Version != config.Version {
		return PulledFile{}, e.New(e.CodeVerifyFailed, "selected Helm archive differs from its config name/version")
	}
	if err := matchHelmChartConfig(archive.Bytes(), config); err != nil {
		return PulledFile{}, err
	}
	err = publishPullDirectory(ctx, out, func(stage *os.File) error {
		return writeImageIndexFile(ctx, stage, object.Name+".tgz", func(destination io.Writer) error {
			_, err := io.Copy(destination, contextReader{ctx: ctx, reader: bytes.NewReader(archive.Bytes())})
			return err
		})
	})
	if err != nil {
		return PulledFile{}, err
	}
	return PulledFile{Path: object.Name + ".tgz", SHA256: manifest.Layers[0].Digest, Size: manifest.Layers[0].Size}, nil
}

func pullAPTOutput(ctx context.Context, reference, out, metadataRoot, workspace string, object PublishedObject, options RegistryOptions) error {
	expectedPath := path.Join(".nvkr", "factory", "packages", object.Name)
	if object.Output == nil || object.Output.Format != "apt-repository" || object.Output.TransportPath != expectedPath {
		return e.New(e.CodeVerifyFailed, "selected APT output has an unsafe transport binding")
	}
	manifest, err := flatManifest(ctx, metadataRoot, object)
	if err != nil {
		return err
	}
	if manifest.ArtifactType != object.ArtifactType || len(manifest.Layers) != 1 || manifest.Layers[0].Digest != object.BlobDigest {
		return e.New(e.CodeVerifyFailed, "selected APT output has no exact single payload")
	}
	file, err := fetchPullLayer(ctx, reference, manifest.Layers[0], workspace, options)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	members, err := inspectPullAPTArchive(ctx, file, manifest.Layers[0], expectedPath)
	if err != nil {
		return err
	}
	repository, err := inspectPullAPTRepository(members)
	if err != nil {
		return err
	}
	components := make([]string, 0, len(repository.Repository.Packages))
	for _, pkg := range repository.Repository.Packages {
		components = append(components, pkg.Name)
	}
	sort.Strings(components)
	if repository.Digest != object.SemanticDigest || !slices.Equal(components, object.Output.Components) {
		return e.New(e.CodeVerifyFailed, "selected APT repository differs from its release identity or components")
	}
	unpacked, err := os.MkdirTemp(workspace, "apt-pull-*")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating private APT inspection workspace")
	}
	unpackedRoot, _, err := securepath.OpenDirectory(unpacked)
	if err != nil {
		_ = os.Remove(unpacked)
		return e.Wrap(e.CodeImmutableViolation, err, "opening private APT inspection workspace")
	}
	defer func() {
		removePullChildren(unpackedRoot)
		_ = unpackedRoot.Close()
		_ = os.Remove(unpacked)
	}()
	if err := transportarchive.UnpackOpen(ctx, file, unpacked, []string{expectedPath}); err != nil {
		return classifyContext(ctx, err)
	}
	if _, err := streamDiskPayload(ctx, file, io.Discard, manifest.Layers[0].Digest, manifest.Layers[0].Size); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "rechecking selected APT carrier"))
	}
	repositoryPath := filepath.Join(unpacked, filepath.FromSlash(expectedPath))
	source, _, err := securepath.OpenDirectory(repositoryPath)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening selected APT repository")
	}
	defer func() { _ = source.Close() }()
	return publishPullDirectory(ctx, out, func(stage *os.File) error {
		seen := make(map[string]bool, len(members))
		if err := copyPullAPTDirectory(ctx, source, stage, "", members, seen); err != nil {
			return err
		}
		if len(seen) != len(members) {
			return e.New(e.CodeVerifyFailed, "selected APT repository changed its archive membership")
		}
		return nil
	})
}

type pullAPTMember struct {
	directory bool
	mode      fs.FileMode
	size      int64
	digest    string
	manifest  []byte
}

// Read semantic identity from the exact carrier bytes, and join each package to
// the same member inventory used for final writes. Reopening an extracted
// manifest independently would let its temporary bytes diverge from that join.
func inspectPullAPTRepository(members map[string]pullAPTMember) (APTResult, error) {
	manifest, ok := members["nvkr-apt-repository.json"]
	if !ok || manifest.directory || len(manifest.manifest) == 0 || len(manifest.manifest) > 8<<20 {
		return APTResult{}, e.New(e.CodeVerifyFailed, "selected APT repository has no bounded canonical manifest")
	}
	var repository APTRepository
	if err := canonical.UnmarshalExact(manifest.manifest, &repository); err != nil {
		return APTResult{}, e.Wrap(e.CodeVerifyFailed, err, "decoding selected APT repository manifest")
	}
	digest, err := repository.Digest()
	if err != nil {
		return APTResult{}, err
	}
	for _, pkg := range repository.Packages {
		member, ok := members[pkg.Filename]
		if !ok || member.directory || member.digest != pkg.SHA256 || member.size != pkg.Size {
			return APTResult{}, e.New(e.CodeVerifyFailed, "selected APT package %q differs from its canonical manifest", pkg.Name)
		}
	}
	return APTResult{Repository: repository, Digest: digest}, nil
}

// Bind every copied file, including APT indexes, to the exact downloaded carrier.
// Package-manifest checks alone do not cover index files or copy-time changes.
func inspectPullAPTArchive(ctx context.Context, file *os.File, descriptor Descriptor, root string) (map[string]pullAPTMember, error) {
	if descriptor.Size < 0 || descriptor.Size > maxDeliveryFileBytes {
		return nil, e.New(e.CodeVerifyFailed, "selected APT carrier exceeds its byte bound")
	}
	hash := sha256.New()
	reader := contextReader{ctx: ctx, reader: io.TeeReader(io.NewSectionReader(file, 0, descriptor.Size), hash)}
	archive := tar.NewReader(reader)
	members := map[string]pullAPTMember{"": {directory: true, mode: 0o755}}
	seen := map[string]bool{}
	var totalSize int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading selected APT carrier"))
		}
		for key := range header.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") {
				return nil, e.New(e.CodeVerifyFailed, "selected APT carrier contains unsupported sparse member %q", header.Name)
			}
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if len(seen) >= 1_000_000 || !safeRelativePath(name) || (name != root && !strings.HasPrefix(name, root+"/")) || seen[name] ||
			(header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir) || header.Mode < 0 || header.Mode > 0o777 ||
			header.Size < 0 || header.Size > maxDeliveryFileBytes || header.Typeflag == tar.TypeDir && (header.Size != 0 || header.Mode&0o500 != 0o500) ||
			header.Typeflag == tar.TypeReg && header.Mode&0o400 == 0 {
			return nil, e.New(e.CodeVerifyFailed, "selected APT carrier has unsafe, duplicate or unsupported member %q", header.Name)
		}
		seen[name] = true
		relative := strings.TrimPrefix(strings.TrimPrefix(name, root), "/")
		member := pullAPTMember{directory: header.Typeflag == tar.TypeDir, mode: fs.FileMode(header.Mode), size: header.Size}
		if previous, ok := members[relative]; ok && !previous.directory {
			return nil, e.New(e.CodeVerifyFailed, "selected APT carrier conflicts with a file parent")
		}
		if relative == "" && !member.directory {
			return nil, e.New(e.CodeVerifyFailed, "selected APT carrier root must be a directory")
		}
		if !member.directory {
			if header.Size > descriptor.Size-totalSize {
				return nil, e.New(e.CodeVerifyFailed, "selected APT carrier exceeds its expanded byte bound")
			}
			totalSize += header.Size
			fileHash := sha256.New()
			var manifest bytes.Buffer
			var destination io.Writer = fileHash
			if relative == "nvkr-apt-repository.json" {
				if header.Size > 8<<20 {
					return nil, e.New(e.CodeVerifyFailed, "selected APT repository manifest exceeds its byte bound")
				}
				destination = io.MultiWriter(fileHash, &manifest)
			}
			if _, err := io.CopyN(destination, contextReader{ctx: ctx, reader: archive}, header.Size); err != nil {
				return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "hashing selected APT member"))
			}
			member.digest = "sha256:" + hex.EncodeToString(fileHash.Sum(nil))
			member.manifest = manifest.Bytes()
		}
		members[relative] = member
		for parent := path.Dir(relative); parent != "." && parent != ""; parent = path.Dir(parent) {
			if previous, ok := members[parent]; ok {
				if !previous.directory {
					return nil, e.New(e.CodeVerifyFailed, "selected APT carrier has a file parent")
				}
			} else {
				members[parent] = pullAPTMember{directory: true, mode: 0o755}
			}
			if len(members) > 1_000_000 {
				return nil, e.New(e.CodeVerifyFailed, "selected APT carrier exceeds its member bound")
			}
		}
	}
	var padding, zeros [32 << 10]byte
	for {
		n, err := reader.Read(padding[:])
		if !bytes.Equal(padding[:n], zeros[:n]) {
			return nil, e.New(e.CodeVerifyFailed, "selected APT carrier contains trailing data")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "checking selected APT carrier padding"))
		}
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != descriptor.Digest {
		return nil, e.New(e.CodeVerifyFailed, "selected APT carrier changed its exact digest")
	}
	return members, nil
}

func copyPullAPTDirectory(ctx context.Context, source, destination *os.File, relative string, members map[string]pullAPTMember, seen map[string]bool) error {
	want, ok := members[relative]
	if !ok || !want.directory {
		return e.New(e.CodeVerifyFailed, "selected APT repository contains an undeclared directory")
	}
	seen[relative] = true
	for {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		entries, readErr := source.ReadDir(256)
		for _, entry := range entries {
			childPath := path.Join(relative, entry.Name())
			child, ok := members[childPath]
			if !ok || seen[childPath] {
				return e.New(e.CodeVerifyFailed, "selected APT repository contains an undeclared member")
			}
			input, info, err := securepath.OpenChild(source, entry.Name(), child.directory)
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "opening selected APT member")
			}
			if info.Mode().Perm() != child.mode || !child.directory && info.Size() != child.size {
				_ = input.Close()
				return e.New(e.CodeVerifyFailed, "selected APT member changed its mode or size")
			}
			if child.directory {
				output, err := createImageIndexDirectory(destination, entry.Name())
				if err != nil {
					_ = input.Close()
					return e.Wrap(e.CodeImmutableViolation, err, "creating selected APT directory")
				}
				err = copyPullAPTDirectory(ctx, input, output, childPath, members, seen)
				_ = input.Close()
				_ = output.Close()
				if err != nil {
					return err
				}
			} else {
				err := copyPullAPTFile(ctx, input, destination, entry.Name(), child)
				_ = input.Close()
				if err != nil {
					return err
				}
				seen[childPath] = true
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return e.Wrap(e.CodeVerifyFailed, readErr, "reading selected APT directory")
		}
	}
	if err := destination.Chmod(want.mode); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "preserving selected APT directory mode")
	}
	return nil
}

func copyPullAPTFile(ctx context.Context, source, directory *os.File, name string, want pullAPTMember) error {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating selected APT file")
	}
	file := os.NewFile(uintptr(fd), name)
	_, copyErr := streamDiskPayload(ctx, source, file, want.digest, want.size)
	if copyErr == nil {
		copyErr = file.Chmod(want.mode)
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "copying exact selected APT file"))
	}
	return nil
}
