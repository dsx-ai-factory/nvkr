// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const qualificationOwnerDirectory = ".github/nvkr-qualification"

var qualificationWorkflowPath = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9_.-]*\.ya?ml$`)

func (manifest qualificationManifest) validate() error {
	if manifest.SchemaVersion != qualificationGeneratedSchema || !canonical.IsDigest(manifest.PlanDigest) || !canonical.IsDigest(manifest.PolicyDigest) || manifest.ProducerWorkflow != factory.GitHubWorkflowPath || len(manifest.Members) != 2 {
		return e.New(e.CodePipelineDrift, "invalid qualification ownership manifest")
	}
	if !qualificationWorkflowPath.MatchString(manifest.QualificationWorkflow) || !qualificationWorkflowPath.MatchString(manifest.SmokeWorkflow) || manifest.QualificationWorkflow == manifest.SmokeWorkflow || manifest.QualificationWorkflow == manifest.ProducerWorkflow || manifest.SmokeWorkflow == manifest.ProducerWorkflow {
		return e.New(e.CodePipelineDrift, "unsafe qualification ownership paths")
	}
	expected := map[string]bool{manifest.QualificationWorkflow: true, manifest.SmokeWorkflow: true}
	previous := ""
	for _, member := range manifest.Members {
		if !expected[member.Path] || member.Path <= previous || member.Mode != 0o644 || !canonical.IsDigest(member.SHA256) {
			return e.New(e.CodePipelineDrift, "invalid qualification ownership member")
		}
		previous = member.Path
	}
	return nil
}

// CheckWorkflows performs a read-only drift check, including modes and the exact
// finite ownership set. It neither creates a lock nor repairs interrupted output.
func CheckWorkflows(ctx context.Context, root string, bundle *GeneratedWorkflows) error {
	if err := generationContext(ctx); err != nil {
		return err
	}
	manifest, err := bundle.manifestBytes()
	if err != nil {
		return err
	}
	fs, err := openQualificationRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = fs.Close() }()
	if err := qualificationDirectorySet(ctx, fs, bundle, false); err != nil {
		return err
	}
	files := bundle.Files()
	files[QualificationManifestPath] = manifest
	for _, name := range qualificationNames(files) {
		raw, present, err := readQualificationFile(ctx, fs, name, 0o644)
		if err != nil {
			return err
		}
		if !present || !bytes.Equal(raw, files[name]) {
			return e.New(e.CodePipelineDrift, "qualification generated file %q differs; regenerate the selected policy", name)
		}
	}
	return nil
}

// CheckGeneratedPolicy admits the checked-in pair against the independent loaded
// policy before candidate I/O. It never recompiles candidate inputs or repairs
// files. The caller supplies its trusted qualifier checkout, not a source Run.
func CheckGeneratedPolicy(ctx context.Context, root string, policy lifecycle.QualificationPolicy) error {
	if err := generationContext(ctx); err != nil {
		return err
	}
	if err := admitRunPolicy(policy); err != nil {
		return err
	}
	digest, err := policy.Digest()
	if err != nil {
		return err
	}
	fs, err := openQualificationRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = fs.Close() }()
	raw, present, err := readQualificationFile(ctx, fs, QualificationManifestPath, 0o644)
	if err != nil {
		return err
	}
	if !present {
		return e.New(e.CodePipelineDrift, "generated qualification manifest is missing; regenerate the selected policy")
	}
	var manifest qualificationManifest
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return e.New(e.CodePipelineDrift, "qualification manifest is not canonical")
	}
	if err := canonical.UnmarshalExact(raw[:len(raw)-1], &manifest); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding qualification manifest")
	}
	if err := manifest.validate(); err != nil {
		return err
	}
	if manifest.PolicyDigest != digest || manifest.ProducerWorkflow != policy.Source.Workflow || manifest.QualificationWorkflow != policy.QualificationWorkflow || manifest.SmokeWorkflow != policy.Smoke.Workflow {
		return e.New(e.CodePipelineDrift, "qualification policy differs from the generated workflow pair")
	}
	bundle := &GeneratedWorkflows{manifest: manifest, files: make(map[string][]byte, 2)}
	for _, member := range manifest.Members {
		raw, present, err := readQualificationFile(ctx, fs, member.Path, 0o644)
		if err != nil {
			return err
		}
		if !present || !bytes.HasPrefix(raw, []byte(qualificationGeneratedHeader)) || pipeline.FileDigest(raw) != member.SHA256 {
			return e.New(e.CodePipelineDrift, "qualification workflow %q differs from its committed manifest", member.Path)
		}
		bundle.files[member.Path] = raw
	}
	return qualificationDirectorySet(ctx, fs, bundle, false)
}

// WriteWorkflows serializes cooperating generators with a kernel-released lock,
// refuses differing unowned or changed bytes, stages the pair and commits the
// manifest last. Exact newly rendered bytes may be adopted to recover an initial
// write interrupted before its first manifest.
// The directory is not an atomic snapshot. Repeating the same generation safely
// recovers a stopped write; --check rejects every incomplete state.
func WriteWorkflows(ctx context.Context, root string, bundle *GeneratedWorkflows) error {
	if err := generationContext(ctx); err != nil {
		return err
	}
	manifest, err := bundle.manifestBytes()
	if err != nil {
		return err
	}
	fs, err := openQualificationRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = fs.Close() }()
	if err := ensureQualificationDirectory(fs, qualificationOwnerDirectory); err != nil {
		return err
	}
	release, err := acquireQualificationLock(ctx, fs, QualificationManifestPath+".lease")
	if err != nil {
		return err
	}
	defer release()
	if err := ensureQualificationDirectory(fs, ".github/workflows"); err != nil {
		return err
	}
	prior, err := qualificationPreflight(ctx, fs, bundle)
	if err != nil {
		return err
	}
	files := bundle.Files()
	files[QualificationManifestPath] = manifest
	names := qualificationNames(bundle.files)
	names = append(names, QualificationManifestPath)
	for _, name := range names {
		if err := stageQualificationFile(ctx, fs, qualificationStage(name, bundle), files[name]); err != nil {
			return err
		}
	}
	if err := syncQualificationDirectory(fs, qualificationOwnerDirectory); err != nil {
		return err
	}
	// Recheck the byte-CAS immediately before publication while holding the owner
	// lock. Cancellation before this point leaves only recoverable bounded stages.
	if _, err := qualificationPreflight(ctx, fs, bundle); err != nil {
		return err
	}
	if err := generationContext(ctx); err != nil {
		return err
	}
	// Once publication begins, finish the finite local commit and directory syncs
	// even if the caller cancels. No subprocess or unbounded external work occurs.
	for _, name := range names[:2] {
		if err := fs.Rename(qualificationStage(name, bundle), name); err != nil {
			return e.Wrap(e.CodeWriteConflict, err, "publishing qualification workflow %q", name)
		}
	}
	for _, member := range prior.Members {
		if _, retained := bundle.files[member.Path]; retained {
			continue
		}
		if err := fs.Remove(member.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return e.Wrap(e.CodeWriteConflict, err, "retiring qualification workflow %q", member.Path)
		}
	}
	if err := syncQualificationDirectory(fs, ".github/workflows"); err != nil {
		return err
	}
	if err := fs.Rename(qualificationStage(QualificationManifestPath, bundle), QualificationManifestPath); err != nil {
		return e.Wrap(e.CodeWriteConflict, err, "committing qualification ownership manifest")
	}
	for _, directory := range []string{qualificationOwnerDirectory, ".github", "."} {
		if err := syncQualificationDirectory(fs, directory); err != nil {
			return err
		}
	}
	return nil
}

func preflightQualificationWrite(ctx context.Context, root string, bundle *GeneratedWorkflows) error {
	if err := generationContext(ctx); err != nil {
		return err
	}
	if _, err := bundle.manifestBytes(); err != nil {
		return err
	}
	fs, err := openQualificationRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = fs.Close() }()
	_, err = qualificationPreflight(ctx, fs, bundle)
	return err
}
func qualificationPreflight(ctx context.Context, fs *os.Root, bundle *GeneratedWorkflows) (qualificationManifest, error) {
	var prior qualificationManifest
	if err := qualificationDirectorySet(ctx, fs, bundle, true); err != nil {
		return prior, err
	}
	raw, present, err := readQualificationFile(ctx, fs, QualificationManifestPath, 0o644)
	if err != nil {
		return prior, err
	}
	if present {
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			return prior, e.New(e.CodePipelineDrift, "qualification manifest is not canonical")
		}
		if err := canonical.UnmarshalExact(raw[:len(raw)-1], &prior); err != nil {
			return prior, e.Wrap(e.CodePipelineDrift, err, "decoding qualification ownership")
		}
		if err := prior.validate(); err != nil {
			return prior, err
		}
	}
	old := map[string]factory.GeneratedMember{}
	for _, member := range prior.Members {
		old[member.Path] = member
	}
	for _, name := range qualificationNames(bundle.files) {
		raw, present, err := readQualificationFile(ctx, fs, name, 0o644)
		if err != nil {
			return prior, err
		}
		if !present {
			continue
		}
		if bytes.Equal(raw, bundle.files[name]) {
			continue
		}
		member, owned := old[name]
		if !owned || pipeline.FileDigest(raw) != member.SHA256 || !bytes.HasPrefix(raw, []byte(qualificationGeneratedHeader)) {
			return prior, e.New(e.CodeWriteConflict, "qualification destination %q is unowned or modified", name)
		}
	}
	for _, member := range prior.Members {
		if _, retained := bundle.files[member.Path]; retained {
			continue
		}
		raw, present, err := readQualificationFile(ctx, fs, member.Path, 0o644)
		if err != nil {
			return prior, err
		}
		if present && (pipeline.FileDigest(raw) != member.SHA256 || !bytes.HasPrefix(raw, []byte(qualificationGeneratedHeader))) {
			return prior, e.New(e.CodeWriteConflict, "retired qualification destination %q was modified", member.Path)
		}
	}
	return prior, nil
}
func qualificationNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func qualificationStage(name string, bundle *GeneratedWorkflows) string {
	suffix := "manifest.stage"
	if name == bundle.manifest.QualificationWorkflow {
		suffix = "qualifier.stage"
	}
	if name == bundle.manifest.SmokeWorkflow {
		suffix = "smoke.stage"
	}
	return qualificationOwnerDirectory + "/" + suffix
}
func openQualificationRoot(root string) (*os.Root, error) {
	if err := qualificationFileSupport(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" {
		return nil, e.New(e.CodeUsage, "qualification generation requires a repository root")
	}
	info, err := os.Lstat(filepath.Clean(root))
	if err != nil {
		return nil, e.Wrap(e.CodeWriteConflict, err, "opening qualification repository root")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, e.New(e.CodeWriteConflict, "qualification repository root is not a real directory")
	}
	fs, err := os.OpenRoot(root)
	if err != nil {
		return nil, e.Wrap(e.CodeWriteConflict, err, "holding qualification repository root")
	}
	return fs, nil
}
func ensureQualificationDirectory(fs *os.Root, name string) error {
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = path.Join(current, part)
		if err := fs.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return e.Wrap(e.CodeWriteConflict, err, "creating qualification directory")
		}
		info, err := fs.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return e.New(e.CodeWriteConflict, "qualification parent %q is not a real directory", current)
		}
	}
	return nil
}
func qualificationParents(fs *os.Root, name string) error {
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(parent, "/") {
		current = path.Join(current, part)
		info, err := fs.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return e.New(e.CodeWriteConflict, "qualification parent %q is not a real directory", current)
		}
	}
	return nil
}
func readQualificationFile(ctx context.Context, fs *os.Root, name string, mode os.FileMode) ([]byte, bool, error) {
	if err := generationContext(ctx); err != nil {
		return nil, false, err
	}
	if err := qualificationParents(fs, name); err != nil {
		return nil, false, err
	}
	before, err := fs.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, e.Wrap(e.CodeWriteConflict, err, "reading qualification file %q", name)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != mode || before.Size() < 0 || before.Size() > maxQualificationWorkflowBytes {
		return nil, false, e.New(e.CodeWriteConflict, "qualification file %q is not a bounded regular file with its owned mode", name)
	}
	file, err := openQualificationFile(fs, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, false, e.Wrap(e.CodeWriteConflict, err, "opening qualification file %q", name)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, false, e.New(e.CodeWriteConflict, "qualification file %q changed while opening", name)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxQualificationWorkflowBytes+1))
	closeErr := file.Close()
	after, statErr := fs.Lstat(name)
	if readErr != nil || closeErr != nil || statErr != nil || len(raw) > maxQualificationWorkflowBytes || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return nil, false, e.New(e.CodeWriteConflict, "qualification file %q changed while reading", name)
	}
	if err := generationContext(ctx); err != nil {
		return nil, false, err
	}
	return raw, true, nil
}
func qualificationDirectorySet(ctx context.Context, fs *os.Root, bundle *GeneratedWorkflows, allowStages bool) error {
	if err := qualificationParents(fs, QualificationManifestPath); err != nil {
		return err
	}
	directory, err := fs.Open(qualificationOwnerDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return e.Wrap(e.CodeWriteConflict, err, "opening qualification owner directory")
	}
	entries, readErr := directory.ReadDir(7)
	closeErr := directory.Close()
	if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil || len(entries) > 5 {
		return e.New(e.CodePipelineDrift, "qualification owner directory exceeds its finite file set")
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "manifest.json":
		case "manifest.json.lease":
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 {
				return e.New(e.CodeWriteConflict, "invalid qualification owner lock")
			}
		case "qualifier.stage", "smoke.stage", "manifest.stage":
			if !allowStages {
				return e.New(e.CodePipelineDrift, "qualification generation is incomplete")
			}
			var expected []byte
			switch entry.Name() {
			case "qualifier.stage":
				expected = bundle.files[bundle.manifest.QualificationWorkflow]
			case "smoke.stage":
				expected = bundle.files[bundle.manifest.SmokeWorkflow]
			case "manifest.stage":
				expected, _ = bundle.manifestBytes()
			}
			raw, _, err := readQualificationFile(ctx, fs, qualificationOwnerDirectory+"/"+entry.Name(), 0o644)
			if err != nil {
				return err
			}
			if !bytes.HasPrefix(expected, raw) {
				return e.New(e.CodeWriteConflict, "qualification stage %q does not belong to this generation", entry.Name())
			}
		default:
			return e.New(e.CodePipelineDrift, "unexpected qualification owner member %q", entry.Name())
		}
	}
	return nil
}
func stageQualificationFile(ctx context.Context, fs *os.Root, name string, raw []byte) error {
	if err := generationContext(ctx); err != nil {
		return err
	}
	existing, present, err := readQualificationFile(ctx, fs, name, 0o644)
	if err != nil {
		return err
	}
	if present && !bytes.HasPrefix(raw, existing) {
		return e.New(e.CodeWriteConflict, "qualification stage changed")
	}
	if present {
		if err := fs.Remove(name); err != nil {
			return e.Wrap(e.CodeWriteConflict, err, "recovering qualification stage")
		}
	}
	file, err := openQualificationFile(fs, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return e.Wrap(e.CodeWriteConflict, err, "creating qualification stage")
	}
	modeErr := file.Chmod(0o644)
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(modeErr, writeErr, syncErr, closeErr); err != nil {
		return e.Wrap(e.CodeWriteConflict, err, "staging qualification bytes")
	}
	return nil
}
func syncQualificationDirectory(fs *os.Root, name string) error {
	directory, err := fs.Open(name)
	if err != nil {
		return e.Wrap(e.CodeWriteConflict, err, "opening qualification directory for sync")
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return e.Wrap(e.CodeWriteConflict, err, "syncing qualification directory")
	}
	return nil
}
