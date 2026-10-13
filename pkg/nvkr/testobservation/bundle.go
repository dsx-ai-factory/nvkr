// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"golang.org/x/sys/unix"
)

// Bundle holds exact observed bytes independently of mutable source paths.
type Bundle struct {
	observation Observation
	snapshot    *Snapshot
	raw         []byte
	digest      string
	passing     bool
}

// Expected must be selected independently by the consumer; a locator supplies no policy.
type Expected struct {
	ObservationDigest string    `json:"observationDigest"`
	Inputs            Inputs    `json:"inputs"`
	Subjects          []Subject `json:"subjects"`
	Suite             Suite     `json:"suite"`
}
type Verification struct {
	ObservationDigest string `json:"observationDigest"`
	SelfConsistent    bool   `json:"selfConsistent"`
	// Passing describes recomputed required-case coverage, not authenticated
	// qualification or release acceptance. Its JSON name preserves that distinction.
	Passing             bool `json:"recordedRequiredCasesPassed"`
	SignerAuthenticated bool `json:"signerAuthenticated"`
}

func clone(o Observation) Observation {
	o.Subjects = slices.Clone(o.Subjects)
	o.Suite.Selection = slices.Clone(o.Suite.Selection)
	o.Suite.RequiredCases = slices.Clone(o.Suite.RequiredCases)
	o.Environment.Facts = slices.Clone(o.Environment.Facts)
	o.Files = slices.Clone(o.Files)
	return o
}

// Observation returns a detached description, never an authority receipt.
func (b *Bundle) Observation() Observation {
	if b == nil {
		return Observation{}
	}
	return clone(b.observation)
}
func (b *Bundle) Canonical(ctx context.Context) ([]byte, error) {
	if b == nil {
		return nil, invalid("observation bundle unavailable")
	}
	if err := b.snapshot.check(ctx); err != nil {
		return nil, err
	}
	return bytes.Clone(b.raw), nil
}
func (b *Bundle) Digest() string {
	if b == nil {
		return ""
	}
	return b.digest
}

// Passing describes recorded case coverage only; Open grants no signer authority.
func (b *Bundle) Passing() bool { return b != nil && b.passing }
func (b *Bundle) Read(ctx context.Context, name string) ([]byte, error) {
	if b == nil {
		return nil, invalid("observation bundle unavailable")
	}
	return b.snapshot.Read(ctx, "files/"+name)
}
func (b *Bundle) Close() error {
	if b == nil {
		return nil
	}
	return b.snapshot.Close()
}

// Emit derives inventories and summaries from pinned selected bytes before
// creating any output. Publication is an atomic no-replace directory commit.
func Emit(ctx context.Context, sourceRoot string, observation Observation, inputs []FileInput, out string) error {
	if err := ValidateDescription(ctx, observation); err != nil {
		return err
	}
	o := clone(observation)
	normalize(&o)
	if len(inputs) >= MaxFiles {
		return invalid("bundle requires at most %d payload files", MaxFiles-1)
	}
	if out == "" {
		return e.New(e.CodeUsage, "observation output directory required")
	}
	for _, f := range inputs {
		if !capturePath("files/" + f.Name) {
			return invalid("covered result path exceeds bundle path limits")
		}
		switch f.Role {
		case "junit", "sbom", "environment", "fixture", "public-log":
		default:
			return invalid("explicit reviewed file role required")
		}
	}
	contents := map[string][]byte{}
	o.Files = []File{}
	if len(inputs) > 0 {
		snapshot, err := Capture(ctx, sourceRoot, inputs)
		if err != nil {
			return err
		}
		defer func() { _ = snapshot.Close() }()
		desc, err := snapshot.Files(ctx)
		if err != nil {
			return err
		}
		roles := map[string]string{}
		for _, f := range inputs {
			roles[f.Name] = f.Role
		}
		for _, f := range desc {
			raw, err := snapshot.Read(ctx, f.Name)
			if err != nil {
				return err
			}
			o.Files = append(o.Files, File{Name: f.Name, Role: roles[f.Name], Digest: f.Digest, Size: f.Size})
			contents[f.Name] = raw
		}
	}

	summary, _, err := assess(ctx, o, func(name string) ([]byte, error) { return contents[name], nil })
	if err != nil {
		return err
	}
	o.Summary = summary
	if err := validate(o, true); err != nil {
		return err
	}
	raw, err := canonical.Marshal(o)
	if err != nil {
		return err
	}
	if len(raw) > MaxObservationBytes {
		return invalid("observation exceeds byte budget")
	}
	return emitDirectory(ctx, out, raw, o.Files, contents)
}

// Open validates canonical bytes, bounded exact directory membership, pinned
// files and recomputed results. It does not authenticate any signer or target.
func Open(ctx context.Context, root string) (*Bundle, error) {
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	dir, rootInfo, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, captureError(ctx, err, "opening observation root")
	}
	defer dir.Close()
	file, info, err := securepath.OpenChild(dir, ObservationFileName, false)
	if err != nil {
		return nil, captureError(ctx, err, "opening canonical observation")
	}
	defer file.Close()
	if info.Size() > MaxObservationBytes {
		return nil, invalid("observation exceeds byte budget")
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxObservationBytes+1))
	if err != nil {
		return nil, captureError(ctx, err, "reading observation")
	}
	if len(raw) > MaxObservationBytes {
		return nil, invalid("observation exceeds byte budget")
	}
	o, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	expected := map[string]bool{ObservationFileName: true}
	inputs := []FileInput{{Name: ObservationFileName, ExpectedDigest: blobDigest(raw)}}
	for _, f := range o.Files {
		key := "files/" + f.Name
		if !capturePath(key) {
			return nil, invalid("bundle path exceeds bounds")
		}
		expected[key] = true
		inputs = append(inputs, FileInput{Name: key, ExpectedDigest: f.Digest})
	}
	// Capture owns MaxFiles files; observation is a control document, so allow
	// at most MaxFiles-1 payload files in this version.
	if len(inputs) > MaxFiles {
		return nil, invalid("bundle has too many covered files")
	}
	if err := checkMembers(ctx, dir, "", expected, new(int)); err != nil {
		return nil, err
	}
	snapshot, err := Capture(ctx, root, inputs)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = snapshot.Close()
		}
	}()
	capturedRaw, err := snapshot.Read(ctx, ObservationFileName)
	if err != nil || !bytes.Equal(raw, capturedRaw) {
		return nil, invalid("observation replaced during bundle capture")
	}
	for _, f := range o.Files {
		v, err := snapshot.Read(ctx, "files/"+f.Name)
		if err != nil {
			return nil, err
		}
		if int64(len(v)) != f.Size {
			return nil, invalid("covered file length differs from observation")
		}
	}
	summary, passing, err := assess(ctx, o, func(name string) ([]byte, error) { return snapshot.Read(ctx, "files/"+name) })
	if err != nil {
		return nil, err
	}
	if summary != o.Summary {
		return nil, invalid("result summary differs from covered JUnit bytes")
	}
	current, currentInfo, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, captureError(ctx, err, "rechecking bundle root")
	}
	_ = current.Close()
	if !os.SameFile(rootInfo, currentInfo) {
		return nil, invalid("bundle root replaced")
	}
	// Repeat exact-membership validation after held capture to reject additions.
	second, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, captureError(ctx, err, "reopening observation root")
	}
	defer second.Close()
	if err := checkMembers(ctx, second, "", expected, new(int)); err != nil {
		return nil, err
	}
	digest, err := canonical.Digest(canonical.KindTestObservation, o)
	if err != nil {
		return nil, err
	}
	accepted = true
	return &Bundle{observation: o, snapshot: snapshot, raw: raw, digest: digest, passing: passing}, nil
}

func VerifyLocal(ctx context.Context, root string, expected Expected) (*Verification, error) {
	if !canonical.IsDigest(expected.ObservationDigest) {
		return nil, invalid("independent observation digest required")
	}
	bundle, err := Open(ctx, root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = bundle.Close() }()
	if err := expected.Check(bundle.Observation()); err != nil {
		return nil, err
	}
	return &Verification{ObservationDigest: bundle.Digest(), SelfConsistent: true, Passing: bundle.passing, SignerAuthenticated: false}, nil
}

func assess(ctx context.Context, o Observation, read func(string) ([]byte, error)) (testresults.Summary, bool, error) {
	summary := testresults.Summary{}
	cases := map[string]testresults.Status{}
	for _, f := range o.Files {
		if f.Role != "junit" {
			continue
		}
		raw, err := read(f.Name)
		if err != nil {
			return summary, false, err
		}
		report, err := testresults.ParseJUnit(ctx, raw)
		if err != nil {
			return summary, false, err
		}
		if report.Summary.Total > testresults.MaxCases-summary.Total {
			return summary, false, invalid("observation exceeds aggregate JUnit case budget")
		}
		summary.Total += report.Summary.Total
		summary.Passed += report.Summary.Passed
		summary.Failed += report.Summary.Failed
		summary.Errors += report.Summary.Errors
		summary.Skipped += report.Summary.Skipped
		for _, c := range report.Cases {
			if c.SuiteNode {
				continue
			}
			key := caseKey(CaseID{Suite: c.Suite, Class: c.Class, Name: c.Name})
			if _, ok := cases[key]; ok {
				return summary, false, invalid("duplicate case identity across covered JUnit files")
			}
			cases[key] = c.Status
		}
	}
	passing := summary.Total > 0 && summary.Failed == 0 && summary.Errors == 0 && len(o.Suite.RequiredCases) > 0 && o.Execution.Status == "completed" && o.Execution.ExitCode == 0 && o.Cleanup.Status == "completed" && o.Cleanup.ExitCode == 0
	for _, required := range o.Suite.RequiredCases {
		status, exists := cases[caseKey(required)]
		if !exists || status != testresults.Passed {
			passing = false
		}
	}
	return summary, passing, nil
}

func checkMembers(ctx context.Context, dir *os.File, prefix string, expected map[string]bool, count *int) error {
	if err := captureContext(ctx); err != nil {
		return err
	}
	entries, err := dir.ReadDir(MaxFiles*maxPathDepth + 2)
	if err != nil && err != io.EOF {
		return captureError(ctx, err, "listing bundle members")
	}
	for _, entry := range entries {
		*count++
		if *count > MaxFiles*maxPathDepth {
			return invalid("too many bundle members")
		}
		name := prefix + entry.Name()
		if !capturePath(name) {
			return invalid("unsafe bundle member")
		}
		isDir := entry.IsDir()
		child, _, err := securepath.OpenChild(dir, entry.Name(), isDir)
		if err != nil {
			return captureError(ctx, err, "opening bundle member")
		}
		if isDir {
			needed := false
			for path := range expected {
				if strings.HasPrefix(path, name+"/") {
					needed = true
					break
				}
			}
			if !needed {
				_ = child.Close()
				return invalid("unexpected bundle directory %q", name)
			}
			err = checkMembers(ctx, child, name+"/", expected, count)
		} else if !expected[name] {
			err = invalid("unexpected bundle member %q", name)
		}
		_ = child.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func blobDigest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func emitDirectory(ctx context.Context, out string, raw []byte, files []File, contents map[string][]byte) error {
	absolute, err := filepath.Abs(out)
	if err != nil {
		return captureError(ctx, err, "resolving observation output")
	}
	parent, parentInfo, err := securepath.OpenDirectory(filepath.Dir(absolute))
	if err != nil {
		return captureError(ctx, err, "opening observation output parent")
	}
	defer parent.Close()
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return captureError(ctx, err, "allocating bundle stage")
	}
	stageName := ".nvkr-observation-" + hex.EncodeToString(token[:])
	if err := unix.Mkdirat(int(parent.Fd()), stageName, 0o700); err != nil {
		return captureError(ctx, err, "creating bundle stage")
	}
	stage, stageInfo, err := securepath.OpenChild(parent, stageName, true)
	if err != nil {
		return captureError(ctx, err, "holding bundle stage")
	}
	defer stage.Close()
	committed := false
	defer func() {
		if !committed {
			_ = clearStage(stage)
			_ = unix.Unlinkat(int(parent.Fd()), stageName, unix.AT_REMOVEDIR)
		}
	}()
	if err := writeMember(ctx, stage, ObservationFileName, raw); err != nil {
		return err
	}
	for _, f := range files {
		if err := writeMember(ctx, stage, "files/"+f.Name, contents[f.Name]); err != nil {
			return err
		}
	}
	if err := syncTree(ctx, stage); err != nil {
		return err
	}
	if err := captureContext(ctx); err != nil {
		return err
	}
	if err := recheckOutputParent(ctx, filepath.Dir(absolute), parentInfo, false); err != nil {
		return err
	}
	namedStage, namedInfo, err := securepath.OpenChild(parent, stageName, true)
	if err != nil {
		return captureError(ctx, err, "rechecking staged observation name")
	}
	_ = namedStage.Close()
	if !os.SameFile(stageInfo, namedInfo) {
		return invalid("staged observation directory was replaced before commit")
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), stageName, int(parent.Fd()), filepath.Base(absolute)); err != nil {
		return captureError(ctx, err, "committing observation without replacement")
	}
	committed = true
	if err := parent.Sync(); err != nil {
		return committedError(ctx, err, "observation committed but persisting parent directory failed")
	}
	if err := recheckOutputParent(ctx, filepath.Dir(absolute), parentInfo, true); err != nil {
		return err
	}
	published, publishedInfo, err := securepath.OpenDirectory(absolute)
	if err != nil {
		return committedError(ctx, err, "observation committed but reopening output directory failed")
	}
	_ = published.Close()
	stageInfo, err = stage.Stat()
	if err != nil {
		return committedError(ctx, err, "observation committed but reading held directory identity failed")
	}
	if !os.SameFile(stageInfo, publishedInfo) {
		return invalid("observation committed but output directory was replaced; no rollback performed")
	}

	committedBundle, err := Open(ctx, absolute)
	if err != nil {
		return committedError(ctx, err, "observation committed but exact bundle recheck failed")
	}
	defer func() { _ = committedBundle.Close() }()
	committedRaw, err := committedBundle.Canonical(ctx)
	if err != nil {
		return committedError(ctx, err, "observation committed but canonical recheck failed")
	}
	if !bytes.Equal(raw, committedRaw) {
		return invalid("observation committed but canonical bytes were replaced; no rollback performed")
	}

	return nil
}
func writeMember(ctx context.Context, root *os.File, name string, raw []byte) error {
	if err := captureContext(ctx); err != nil {
		return err
	}
	parent, leaf, err := securepath.CreateParent(root, name)
	if err != nil {
		return captureError(ctx, err, "creating bundle member parent")
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), leaf, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return captureError(ctx, err, "creating bundle member")
	}
	file := os.NewFile(uintptr(fd), leaf)
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return captureError(ctx, err, "writing bundle member")
	}
	if err := file.Sync(); err != nil {
		return captureError(ctx, err, "persisting bundle member")
	}
	return nil
}
func syncTree(ctx context.Context, dir *os.File) error {
	if _, err := dir.Seek(0, 0); err != nil {
		return captureError(ctx, err, "rewinding bundle directory")
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return captureError(ctx, err, "listing staged bundle")
	}
	for _, entry := range entries {
		if err := captureContext(ctx); err != nil {
			return err
		}
		if entry.IsDir() {
			child, _, err := securepath.OpenChild(dir, entry.Name(), true)
			if err != nil {
				return captureError(ctx, err, "holding staged directory")
			}
			err = syncTree(ctx, child)
			_ = child.Close()
			if err != nil {
				return err
			}
		}
	}
	if err := dir.Sync(); err != nil {
		return captureError(ctx, err, "persisting bundle directory")
	}
	return nil
}
func clearStage(dir *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return securepath.ClearDirectory(ctx, dir)
}

func decodeVersion(raw []byte, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "dispatching observation schema")
	}
	return nil
}

func recheckOutputParent(ctx context.Context, path string, original os.FileInfo, committed bool) error {
	detail := "rechecking observation output parent"
	if committed {
		detail = "observation committed but rechecking output parent failed; bundle may remain beneath original parent"
	}
	current, info, err := securepath.OpenDirectory(path)
	if err != nil {
		if committed {
			return committedError(ctx, err, detail)
		}
		return captureError(ctx, err, detail)
	}
	_ = current.Close()
	if !os.SameFile(original, info) {
		return invalid("%s: parent was replaced", detail)
	}
	return nil
}

func committedError(ctx context.Context, err error, operation string) error {
	code := e.CodeVerifyFailed
	if ctx != nil && ctx.Err() != nil {
		code = e.CodeTimeout
	}
	return e.Wrap(code, err, "%s; no rollback performed", operation)
}
