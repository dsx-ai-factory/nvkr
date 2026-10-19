// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const localEvidenceBundleLimit = 128 << 20

// The envelope carries evidence only, never payloads, trust defaults or receipts.
type localEvidenceBundle struct {
	SchemaVersion string            `json:"schemaVersion"`
	Files         map[string][]byte `json:"files"`
}

// writeEvidenceBundle reports publication separately from durability errors so
// callers can identify a surviving destination without claiming success.
func (qualified *localVerifiedQualification) writeEvidenceBundle(ctx context.Context, path string) (published bool, err error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return false, e.New(e.CodeEngineCapability, "local evidence publication requires no-replace filesystem support")
	}
	if path == "" {
		return false, e.New(e.CodeUsage, "local evidence destination is required")
	}
	if qualified == nil {
		return false, e.New(e.CodeGateUnmet, "local qualification verification is not live")
	}
	operation, finish, err := qualified.build.beginOperation(ctx)
	if err != nil {
		return false, err
	}
	defer finish()
	raw, err := qualified.evidenceBundleBytes(operation)
	if err != nil {
		return false, err
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "opening evidence destination parent")
	}
	defer func() {
		if closeErr := parent.Close(); closeErr != nil {
			err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, closeErr, "closing evidence destination parent"))
		}
	}()
	file, temporary, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "staging local evidence bundle")
	}
	defer func() {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, closeErr, "closing evidence stage"))
			}
		}
		if temporary != "" {
			if cleanupErr := unix.Unlinkat(int(parent.Fd()), temporary, 0); cleanupErr != nil {
				err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, cleanupErr, "removing local evidence stage"))
			}
		}
	}()
	for len(raw) > 0 {
		if err := operation.Err(); err != nil {
			return false, e.Wrap(e.CodeTimeout, err, "writing local evidence canceled")
		}
		chunk := min(len(raw), 64<<10)
		written, err := file.Write(raw[:chunk])
		if err != nil {
			return false, e.Wrap(e.CodeImmutableViolation, err, "writing local evidence bundle")
		}
		if written != chunk {
			return false, e.New(e.CodeImmutableViolation, "short local evidence bundle write")
		}
		raw = raw[chunk:]
	}
	if err := file.Chmod(0o444); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "protecting local evidence bundle")
	}
	if err := file.Sync(); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "syncing local evidence bundle")
	}
	closeErr := file.Close()
	file = nil
	if closeErr != nil {
		return false, e.Wrap(e.CodeImmutableViolation, closeErr, "closing local evidence bundle")
	}
	if err := qualified.requireLive(operation); err != nil {
		return false, err
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), filepath.Base(path)); err != nil {
		return false, e.Wrap(e.CodeImmutableViolation, err, "publishing local evidence bundle")
	}
	temporary = ""
	if err := parent.Sync(); err != nil {
		return true, e.Wrap(e.CodeImmutableViolation, err, "syncing local evidence destination")
	}
	return true, nil
}

func readLocalEvidenceBundle(ctx context.Context, path string) (map[string][]byte, error) {
	if ctx == nil || path == "" {
		return nil, e.New(e.CodeUsage, "local evidence loading requires context and path")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local evidence loading canceled")
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening local evidence parent")
	}
	defer parent.Close()
	raw, err := readLocalQualificationMetadata(ctx, parent, filepath.Base(path), localEvidenceBundleLimit)
	if err != nil {
		return nil, err
	}
	return decodeLocalEvidenceBundle(raw)
}

func decodeLocalEvidenceBundle(raw []byte) (map[string][]byte, error) {
	if len(raw) == 0 || len(raw) > localEvidenceBundleLimit {
		return nil, e.New(e.CodeVerifyFailed, "local evidence bundle is empty or oversized")
	}
	var bundle localEvidenceBundle
	if err := canonical.UnmarshalExact(raw, &bundle); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding local evidence bundle")
	}
	if bundle.SchemaVersion != "nvkr.dev/local-evidence-bundle/v1" {
		return nil, e.New(e.CodeVerifyFailed, "unsupported local evidence bundle schema")
	}
	return freezeLocalEvidenceFiles(bundle.Files)
}

func (qualified *localVerifiedQualification) evidenceBundleBytes(ctx context.Context) ([]byte, error) {
	files, err := qualified.evidenceFiles(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := freezeLocalEvidenceFiles(files); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(localEvidenceBundle{SchemaVersion: "nvkr.dev/local-evidence-bundle/v1", Files: files})
	if err != nil {
		return nil, err
	}
	if len(raw) > localEvidenceBundleLimit {
		return nil, e.New(e.CodeVerifyFailed, "local evidence bundle exceeds transport limit")
	}
	return raw, nil
}
