// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package toolchainmaintenance owns the internal, candidate-only image refresh.
// Reviewed main-branch records authorize exact subjects, never general clearance.
package toolchainmaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const (
	Repository        = "dsx-ai-factory/nvkr"
	Registry          = "ghcr.io/" + Repository + "/m2-candidate/toolchains"
	Owner             = "njhensley"
	Scope             = "internal-m2-candidate-only"
	Schema            = "nvkr.dev/toolchain-refresh/v1"
	Workflow          = ".github/workflows/nvkr-native-toolchains.yml"
	ApprovalDirectory = "inputs/policies/toolchain-maintenance"
	maxRecordBytes    = 1 << 20
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,38}$`)
var imageKeys = []string{"amd64/inspector", "amd64/packer", "arm64/inspector", "arm64/packer"}

// Record describes a complete preparation attempt. It is not an authority receipt.
// The workflow accepts this document only from its reviewed main-branch checkout.
type Record struct {
	SchemaVersion string  `json:"schemaVersion"`
	SourceCommit  string  `json:"sourceCommit"`
	RunID         int64   `json:"runID"`
	Attempt       int     `json:"attempt"`
	Images        []Image `json:"images"`
}

type Image struct {
	Arch              string            `json:"arch"`
	Kind              string            `json:"kind"`
	ArchiveDigest     string            `json:"archiveDigest"`
	ImageID           string            `json:"imageID"`
	ManifestDigest    string            `json:"manifestDigest"`
	MeasurementDigest string            `json:"measurementDigest"`
	Executables       map[string]string `json:"executables,omitempty"`
	Exception         Exception         `json:"exception"`
}

type Exception struct {
	Approver       string `json:"approver"`
	Scope          string `json:"scope"`
	Expires        string `json:"expires"`
	LicenseReview  string `json:"licenseReview"`
	SecurityReview string `json:"securityReview"`
}

func (im Image) Key() string       { return im.Arch + "/" + im.Kind }
func (im Image) Reference() string { return Registry + "/" + im.Kind + "-native@" + im.ManifestDigest }

// ExecutablePaths is the existing inspector profile mapping, including helper
// admission evidence even though the Environment does not pin the helper alone.
var executablePaths = map[string]string{
	"inspectorDigest":     "/usr/bin/virt-inspector",
	"codecDigest":         "/usr/bin/qemu-img",
	"guestfishDigest":     "/usr/bin/guestfish",
	"scannerDigest":       "/usr/local/bin/syft",
	"packageReaderDigest": "/usr/bin/dpkg-deb",
	"helperDigest":        "/usr/local/bin/nvkr-disk-inspect",
}

func read(ctx context.Context, path string) ([]byte, error) {
	f, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "toolchain maintenance record", MaxBytes: maxRecordBytes})
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "read maintenance record")
	}
	defer func() { _ = f.Close() }()
	b, err := f.ReadAllContext(ctx, maxRecordBytes)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "read maintenance bytes")
	}
	return b, nil
}

func decode(raw []byte, into any) error {
	// Reject every duplicate, including aliases that encoding/json folds to the
	// same struct field, before decoding any approval or measurement values.
	if err := canonical.ValidateFoldedJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "ambiguous maintenance record")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "decode maintenance record")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return e.New(e.CodeInvalidCatalog, "trailing maintenance data")
	}
	return nil
}

func (r Record) validate(now time.Time, approved bool) error {
	if r.SchemaVersion != Schema || !commitPattern.MatchString(r.SourceCommit) || r.RunID <= 0 || r.Attempt <= 0 || len(r.Images) != 4 {
		return e.New(e.CodeInvalidCatalog, "refresh requires one exact source attempt and four native images")
	}
	keys := make([]string, 0, 4)
	for _, im := range r.Images {
		keys = append(keys, im.Key())
		for _, digest := range []string{im.ArchiveDigest, im.ImageID, im.ManifestDigest, im.MeasurementDigest} {
			if !canonical.IsDigest(digest) {
				return e.New(e.CodeUnpinnedInput, "invalid %s subject digest", im.Key())
			}
		}
		if im.Kind == "inspector" {
			if len(im.Executables) != len(executablePaths) {
				return e.New(e.CodeVerifyFailed, "incomplete inspector measurements")
			}
			for field := range executablePaths {
				if !canonical.IsDigest(im.Executables[field]) {
					return e.New(e.CodeVerifyFailed, "missing inspector executable %s", field)
				}
			}
		} else if len(im.Executables) != 0 {
			return e.New(e.CodeInvalidCatalog, "unexpected executable measurements")
		}
		if approved {
			x := im.Exception
			expiry, err := time.Parse(time.RFC3339, x.Expires)
			if !userPattern.MatchString(x.Approver) || x.Scope != Scope || err != nil || !expiry.After(now) || !reviewReference(x.LicenseReview) || !reviewReference(x.SecurityReview) {
				return e.New(e.CodeGateUnmet, "%s needs a reviewed subject-bound license/security exception with approver, candidate scope, future expiry and review references", im.Key())
			}
		}
	}
	if !slices.Equal(keys, imageKeys) {
		return e.New(e.CodeReleaseIncomplete, "refresh images must be sorted, unique and cover both native architectures")
	}
	return nil
}

func reviewReference(value string) bool {
	return strings.HasPrefix(value, "https://") && len(value) > 8 && len(value) <= 2048 && !strings.ContainsAny(value, "\r\n\t ")
}

// LoadApproval reads only a reviewable, run-named policy path. Authentication of
// the default-branch workflow and preparation run is performed by Admit below.
func LoadApproval(ctx context.Context, root, run string, now time.Time) (Record, error) {
	var record Record
	if !regexp.MustCompile(`^[1-9][0-9]{0,18}$`).MatchString(run) {
		return record, e.New(e.CodeUsage, "approval requires a decimal preparation run ID")
	}
	raw, err := read(ctx, filepath.Join(root, ApprovalDirectory, run+".json"))
	if err != nil {
		return record, err
	}
	if err = decode(raw, &record); err != nil {
		return record, err
	}
	if fmt.Sprint(record.RunID) != run {
		return record, e.New(e.CodeUnverifiedRef, "approval filename and run disagree")
	}
	return record, record.validate(now, true)
}
