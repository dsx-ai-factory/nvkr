// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/rootfs"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

// inspectMkosiOutput checks the actual packaged layer, including the unchanged
// original reports, exact recipe and selected firmware/executable measurements.
// A successful check describes bytes; only the concrete execution owner may
// subsequently issue production authority.
func inspectMkosiOutput(ctx context.Context, node ir.Node, layout, inputs, scratch string) (subject string, size int64, returnErr error) {
	recipe, err := canonical.Marshal(node.Mkosi.Logical())
	if err != nil {
		return "", 0, err
	}
	for _, selected := range []struct{ name, digest string }{
		{"recipe.json", FileDigest(recipe)}, {"mkosi.conf", node.Mkosi.ConfigurationDigest},
	} {
		got, _, _, err := hashRegularBeneathContext(ctx, inputs, "evidence"+mkosi.EvidenceDirectory+"/"+selected.name)
		if err != nil || got != selected.digest {
			return "", 0, e.New(e.CodeVerifyFailed, "retained runtime recipe changed from its selected input")
		}
	}
	tree, err := ociclosure.Snapshot(ctx, layout)
	if err != nil {
		return "", 0, e.Wrap(e.CodeVerifyFailed, err, "capturing mkosi OCI result")
	}
	raw, err := tree.ReadRegular(ctx, "index.json", 1<<20)
	if err != nil {
		return "", 0, e.Wrap(e.CodeVerifyFailed, err, "reading mkosi OCI index")
	}
	var index ociclosure.IndexDocument
	if err := ociclosure.DecodeJSON(raw, &index); err != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 {
		return "", 0, e.New(e.CodeVerifyFailed, "mkosi result requires exactly one native OCI image")
	}
	subject = index.Manifests[0].Digest
	if !canonical.IsDigest(subject) {
		return "", 0, e.New(e.CodeVerifyFailed, "mkosi result has no exact manifest identity")
	}
	layer, err := rootfs.CaptureLayer(ctx, layout, subject, node.Platform)
	if err != nil {
		return "", 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, layer.Close()) }()
	staged, err := rootfs.Stage(ctx, layer, scratch)
	if err != nil {
		return "", 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, staged.Close()) }()
	directory, err := staged.Directory()
	if err != nil {
		return "", 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	// The root descriptor remains held until every relative open and hash ends.
	measurements := append([]mkosi.FileMeasurement(nil), node.Mkosi.Measurements...)
	modes := map[string]os.FileMode{}
	for _, copied := range node.Mkosi.Copies {
		measurements = append(measurements, mkosi.FileMeasurement{Path: copied.Path, Digest: copied.Digest})
		modes[copied.Path] = os.FileMode(copied.Mode)
	}
	for _, content := range node.Mkosi.Contents {
		measurements = append(measurements, mkosi.FileMeasurement{Path: content.Path, Digest: content.Digest})
		modes[content.Path] = 0o444
	}
	for _, measurement := range measurements {
		got, _, mode, err := hashMkosiRootFile(ctx, directory, strings.TrimPrefix(measurement.Path, "/"))
		if err != nil {
			return "", 0, e.Wrap(e.CodeVerifyFailed, err, "measuring runtime file %s", measurement.Path)
		}
		if got != measurement.Digest || modes[measurement.Path] != 0 && mode != modes[measurement.Path] {
			return "", 0, e.New(e.CodeVerifyFailed, "runtime file %s differs from its exact measurement", measurement.Path)
		}
	}
	for _, projection := range node.Mkosi.Files {
		want, wantSize, _, err := hashRegularBeneathContext(ctx, inputs, "file-materials"+projection.Path)
		if err != nil {
			return "", 0, e.Wrap(e.CodeVerifyFailed, err, "rechecking original runtime file material")
		}
		got, gotSize, mode, err := hashMkosiRootFile(ctx, directory, strings.TrimPrefix(projection.Path, "/"))
		if err != nil || got != want || gotSize != wantSize || mode != os.FileMode(projection.Mode) {
			return "", 0, e.New(e.CodeVerifyFailed, "packaged runtime file %s differs from its material bytes", projection.Path)
		}
	}
	reports := []string{"recipe.json", "mkosi.conf"}
	for _, root := range node.Mkosi.Roots {
		reports = append(reports, root.Name+".manifest.json")
	}
	for _, name := range reports {
		relative := strings.TrimPrefix(mkosi.EvidenceDirectory, "/") + "/" + name
		want, wantSize, _, err := hashRegularBeneathContext(ctx, inputs, filepath.Join("evidence", relative))
		if err != nil {
			return "", 0, e.Wrap(e.CodeVerifyFailed, err, "rechecking original runtime evidence")
		}
		got, gotSize, _, err := hashMkosiRootFile(ctx, directory, relative)
		if err != nil || got != want || gotSize != wantSize {
			return "", 0, e.New(e.CodeVerifyFailed, "packaged runtime evidence %s differs from its original bytes", name)
		}
	}
	size, err = tree.Size(ctx)
	if err != nil {
		return "", 0, e.Wrap(e.CodeVerifyFailed, err, "observing mkosi result size")
	}
	return subject, size, nil
}

func hashMkosiRootFile(ctx context.Context, root *os.File, relative string) (string, int64, os.FileMode, error) {
	file, info, err := openMkosiRootFile(ctx, root, relative)
	if err != nil {
		return "", 0, 0, err
	}
	digest, size, _, hashErr := hashOpenRegularContext(ctx, file, info)
	return digest, size, info.Mode().Perm(), errors.Join(hashErr, file.Close())
}

func openMkosiRootFile(ctx context.Context, root *os.File, relative string) (*os.File, os.FileInfo, error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "runtime file opening requires context")
	}
	parts := strings.Split(relative, "/")
	current := root
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			if current != root {
				_ = current.Close()
			}
			return nil, nil, e.Wrap(e.CodeTimeout, err, "opening runtime file")
		}
		next, info, err := securepath.OpenChild(current, part, index != len(parts)-1)
		if current != root {
			_ = current.Close()
		}
		if err != nil {
			return nil, nil, err
		}
		if index == len(parts)-1 {
			return next, info, nil
		}
		current = next
	}
	return nil, nil, e.New(e.CodeVerifyFailed, "runtime file path is empty")
}
