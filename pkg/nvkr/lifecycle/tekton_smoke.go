// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"golang.org/x/sys/unix"
)

// WriteNativeSmoke accepts only the live shared execution receipt. The separate
// smoke role signs exact successful observations; the factory publishes their
// byte-authenticated carrier only after this operation and cleanup succeed.
func (session *TektonQualification) WriteNativeSmoke(ctx context.Context, receipt *NativeToolSmokeReceipt) error {
	if session == nil || session.Invocation == nil {
		return e.New(e.CodeGateUnmet, "Tekton smoke has no live owner")
	}
	current, err := session.Invocation.Description()
	if err != nil {
		return err
	}
	if current.Task != "nvkr-smoke" {
		return e.New(e.CodeUntrustedSigner, "Tekton smoke signer requires its separate worker")
	}
	report, err := receipt.Description()
	if err != nil {
		return err
	}
	checks, err := session.Checks.Description()
	if err != nil {
		return err
	}
	digest, err := session.policy.Digest()
	if err != nil {
		return err
	}
	if report.Invocation != current || report.PolicyDigest != digest || !sameSmokeChecks(report.ArtifactChecks, checks) {
		return e.New(e.CodeGateUnmet, "Tekton smoke receipt differs from its active worker")
	}
	raw, err := report.Canonical()
	if err != nil {
		return err
	}
	key, err := session.task.SigningKey(ctx)
	if err != nil {
		return err
	}
	defer clear(key)
	signer, err := sign.NewLocalCosign(session.binary, session.binaryDigest)
	if err != nil {
		return err
	}
	signed, err := signer.SignDocument(ctx, raw, key, []byte(session.policy.Tekton.SmokeKey))
	if err != nil {
		return err
	}
	signature, err := signed.Signature()
	if err != nil {
		return err
	}
	if _, err := receipt.Description(); err != nil {
		return err
	}
	directory, err := session.task.EvidenceDirectory(ctx, "smoke")
	if err != nil {
		return err
	}
	return WriteTektonEvidence(ctx, directory, map[string][]byte{"report.json": raw, "signature": signature})
}

// AuthenticateNativeSmoke requires both the exact successful provider carrier
// and the independently selected worker key. It recomputes membership from the
// fresh artifact-check receipt before issuing the existing complete smoke receipt.
func (session *TektonQualification) AuthenticateNativeSmoke(ctx context.Context) (*ToolSmokeReceipt, error) {
	if session == nil || session.Invocation == nil {
		return nil, e.New(e.CodeGateUnmet, "Tekton qualification has no live owner")
	}
	if err := session.Invocation.RequireSigning(); err != nil {
		return nil, err
	}
	operation, finish, err := session.Checks.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx = operation
	directory, err := session.task.EvidenceDirectory(ctx, "smoke")
	if err != nil {
		return nil, err
	}
	raw, err := ReadTektonEvidence(ctx, filepath.Join(directory, "report.json"))
	if err != nil {
		return nil, err
	}
	signature, err := ReadTektonEvidence(ctx, filepath.Join(directory, "signature"))
	if err != nil {
		return nil, err
	}
	verifier, err := sign.NewLocalCosign(session.binary, session.binaryDigest)
	if err != nil {
		return nil, err
	}
	if err := verifier.VerifyDocument(ctx, raw, signature, []byte(session.policy.Tekton.SmokeKey)); err != nil {
		return nil, err
	}
	var report NativeToolSmokeReport
	if err := canonical.UnmarshalExact(raw, &report); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding exact Tekton smoke report")
	}
	exact, err := report.Canonical()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(raw, exact) {
		return nil, e.New(e.CodeVerifyFailed, "Tekton smoke report is not canonical")
	}
	uid, err := session.task.CompletedTaskUID(ctx, "nvkr-smoke")
	if err != nil {
		return nil, err
	}
	checks, err := session.Checks.Description()
	if err != nil {
		return nil, err
	}
	current, err := session.Invocation.Description()
	if err != nil {
		return nil, err
	}
	digest, err := session.policy.Digest()
	if err != nil {
		return nil, err
	}
	if report.Invocation.TaskUID != uid || report.PolicyDigest != digest || report.Image != session.policy.Smoke.Images[session.policy.Tekton.Platform] || !sameSmokeChecks(report.ArtifactChecks, checks) {
		return nil, e.New(e.CodeGateUnmet, "Tekton smoke report substituted its provider task, policy or artifact checks")
	}
	if err := matchSmokeWorkerInvocation(report.Invocation, current); err != nil {
		return nil, err
	}
	members, err := qualificationSmokeMembers(session.Checks, session.policy)
	if err != nil {
		return nil, err
	}
	if err := matchSmokeMembers(report, members); err != nil {
		return nil, err
	}
	full := ToolSmokeDescription{ArtifactChecks: checks, Invocation: current, PolicyDigest: digest, Policy: session.policy, Workers: []NativeToolSmokeReport{report}}
	if err := full.Validate(); err != nil {
		return nil, err
	}
	encoded, err := canonical.Marshal(full)
	if err != nil {
		return nil, err
	}
	if err := session.Invocation.RequireSource(checks.Origin); err != nil {
		return nil, err
	}
	return &ToolSmokeReceipt{raw: string(encoded), checks: session.Checks, invocation: session.Invocation, closed: new(atomic.Bool)}, nil
}

// WriteTektonEvidence creates a fresh bounded directory in the owned task
// workspace. The factory alone chooses the outgoing carrier membership.
func WriteTektonEvidence(ctx context.Context, directory string, files map[string][]byte) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "Tekton evidence requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "Tekton evidence write canceled")
	}
	if directory != "/work/.nvkr/tekton/smoke" && directory != "/work/.nvkr/tekton/qualification" && directory != "/work/.nvkr/tekton/result" {
		return e.New(e.CodeUsage, "Tekton evidence requires its fixed task directory")
	}
	if len(files) == 0 || len(files) > 8 {
		return e.New(e.CodeVerifyFailed, "Tekton evidence file count exceeds its bound")
	}
	for name, raw := range files {
		if filepath.Base(name) != name || name == "." || name == ".." || len(raw) == 0 || int64(len(raw)) > sign.MaxQualificationPredicateBytes {
			return e.New(e.CodeVerifyFailed, "Tekton evidence has an unsafe name or size")
		}
	}
	parent, _, err := securepath.OpenDirectory("/work")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "holding Tekton workspace")
	}
	defer func() { _ = parent.Close() }()
	parts := strings.Split(strings.TrimPrefix(directory, "/work/"), "/")
	for i, part := range parts {
		err := unix.Mkdirat(int(parent.Fd()), part, 0700)
		if err != nil && (i == len(parts)-1 || !errors.Is(err, unix.EEXIST)) {
			return e.Wrap(e.CodeImmutableViolation, err, "creating fresh Tekton evidence directory")
		}
		child, _, err := securepath.OpenChild(parent, part, true)
		if err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "holding Tekton evidence directory without symlinks")
		}
		if err := parent.Sync(); err != nil {
			_ = child.Close()
			return e.Wrap(e.CodeImmutableViolation, err, "committing Tekton evidence directory")
		}
		_ = parent.Close()
		parent = child
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "Tekton evidence write canceled")
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "creating Tekton evidence file")
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			_ = unix.Close(fd)
			return e.New(e.CodeImmutableViolation, "holding Tekton evidence output")
		}
		_, writeErr := file.Write(files[name])
		err = errors.Join(writeErr, file.Sync(), file.Close())
		if err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "committing Tekton evidence file")
		}
	}
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing complete Tekton evidence")
	}
	return nil
}

func ReadTektonEvidence(ctx context.Context, path string) ([]byte, error) {
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "Tekton evidence", MaxBytes: sign.MaxQualificationPredicateBytes})
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening bounded Tekton evidence")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, sign.MaxQualificationPredicateBytes)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading exact Tekton evidence")
	}
	return raw, nil
}
