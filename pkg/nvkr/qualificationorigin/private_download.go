// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type privateArtifactDownload func(context.Context, cistore.Request, cistore.Locator, string, cistore.Options) error

// resolvePrivateArtifact interprets only a provider-verified locator ZIP. A
// recognized locator never falls back to a public payload or another transport.
// Its returned bytes remain descriptions; downstream signature and membership
// verification still owns qualification authority.
func (g *GitHub) resolvePrivateArtifact(ctx context.Context, require func() error, origin Description, name string, archive []byte, maxArchiveBytes int64, limits map[string]int64) (members map[string][]byte, recognized bool, resultErr error) {
	bounded := &handoffZIPReaderAt{ctx: ctx, reader: bytes.NewReader(archive), metadata: true, remaining: maxQualificationZIPMetadataBytes}
	reader, err := zip.NewReader(bounded, int64(len(archive)))
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, e.Wrap(e.CodeTimeout, ctx.Err(), "private artifact locator decoding canceled")
		}
		return nil, false, e.Wrap(e.CodeVerifyFailed, err, "invalid provider artifact ZIP")
	}
	locatorName := name + ".ci-locator.json"
	for _, member := range reader.File {
		if member.Name == locatorName {
			recognized = true
		}
	}
	if !recognized {
		return nil, false, nil
	}
	if len(reader.File) != 1 || !reader.File[0].Mode().IsRegular() || reader.File[0].UncompressedSize64 > cistore.MaxLocatorBytes {
		return nil, true, e.New(e.CodeVerifyFailed, "private artifact requires exactly one bounded regular locator")
	}
	bounded.metadata = false
	input, err := reader.File[0].Open()
	if err != nil {
		return nil, true, e.Wrap(e.CodeVerifyFailed, err, "opening private artifact locator")
	}
	raw, readErr := io.ReadAll(io.LimitReader(handoffContextReader{ctx: ctx, reader: input}, cistore.MaxLocatorBytes+1))
	if err = errors.Join(readErr, input.Close()); err != nil {
		if ctx.Err() != nil {
			return nil, true, e.Wrap(e.CodeTimeout, ctx.Err(), "private artifact locator read canceled")
		}
		return nil, true, e.Wrap(e.CodeVerifyFailed, err, "reading private artifact locator")
	}
	if len(raw) > cistore.MaxLocatorBytes || uint64(len(raw)) != reader.File[0].UncompressedSize64 {
		return nil, true, e.New(e.CodeVerifyFailed, "private artifact locator exceeds its declared byte bound")
	}
	locator, err := cistore.DecodeLocator(raw)
	if err != nil {
		return nil, true, err
	}
	request := cistore.Request{Repository: origin.Repository, RunID: origin.RunID, Attempt: origin.Attempt, Name: name}
	if err = locator.Validate(request); err != nil {
		return nil, true, err
	}
	if locator.ArchiveSize > maxArchiveBytes {
		return nil, true, e.New(e.CodeVerifyFailed, "private artifact locator differs from exact origin or payload bound")
	}
	if g.storageToken == "" || strings.TrimSpace(g.storageUsername) == "" || g.downloadPrivate == nil {
		return nil, true, e.New(e.CodeEngineCapability, "private CI payload retrieval requires an explicit storage username and token")
	}
	if err = ctx.Err(); err != nil {
		return nil, true, e.Wrap(e.CodeTimeout, err, "private artifact download canceled")
	}
	if err = require(); err != nil {
		return nil, true, err
	}
	root, err := os.MkdirTemp("", "nvkr-origin-private-")
	if err != nil {
		return nil, true, e.Wrap(e.CodeFetchFailed, err, "creating private artifact staging directory")
	}
	defer func() {
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
			members = nil
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeFetchFailed, cleanupErr, "removing private artifact staging directory"))
		}
	}()
	destination := filepath.Join(root, "payload")
	if err = g.downloadPrivate(ctx, request, locator, destination, cistore.Options{Token: g.storageToken, Username: g.storageUsername, RepositoryToken: g.token}); err != nil {
		return nil, true, err
	}
	if err = require(); err != nil {
		return nil, true, err
	}
	members, err = readPrivateArtifactMembers(ctx, destination, limits)
	if err != nil {
		return nil, true, err
	}
	if err = require(); err != nil {
		return nil, true, err
	}
	return members, true, nil
}

func readPrivateArtifactMembers(ctx context.Context, root string, limits map[string]int64) (map[string][]byte, error) {
	directory, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening restored private artifact directory")
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(len(limits) + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading restored private artifact membership")
	}
	if len(entries) != len(limits) {
		return nil, e.New(e.CodeVerifyFailed, "private artifact differs from exact expected file membership")
	}
	members := make(map[string][]byte, len(limits))
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "restored private artifact read canceled")
		}
		limit, ok := limits[entry.Name()]
		if !ok {
			return nil, e.New(e.CodeVerifyFailed, "private artifact includes an unexpected member")
		}
		file, info, openErr := securepath.OpenChild(directory, entry.Name(), false)
		if openErr != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, openErr, "holding restored private artifact member")
		}
		if info.Size() < 0 || info.Size() > limit {
			_ = file.Close()
			return nil, e.New(e.CodeVerifyFailed, "private artifact member exceeds its byte bound")
		}
		raw, readErr := io.ReadAll(io.LimitReader(handoffContextReader{ctx: ctx, reader: file}, limit+1))
		if err = errors.Join(readErr, file.Close()); err != nil {
			if ctx.Err() != nil {
				return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "private artifact member read canceled")
			}
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading restored private artifact member")
		}
		if int64(len(raw)) != info.Size() || int64(len(raw)) > limit {
			return nil, e.New(e.CodeVerifyFailed, "private artifact member changed or exceeds its bound")
		}
		members[entry.Name()] = raw
	}
	return members, nil
}
