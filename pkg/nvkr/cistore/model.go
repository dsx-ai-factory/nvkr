// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package cistore transports payload carriers through private GHCR and validated
// metadata through Actions. Records describe transport bytes, never release or
// build authority. Consumers retain their own artifact authentication checks.
package cistore

import (
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

const (
	// SchemaVersion identifies this CI transport metadata format.
	SchemaVersion = "nvkr.dev/ci-carrier/v2"
	// BindingLabel joins the OCI config to all locator fields except Reference.
	BindingLabel = "nvkr.dev/ci-carrier"
	// MaxLocatorBytes bounds private v2 locators; public metadata has its own bound.
	MaxLocatorBytes = 32 << 10
)

// Request identifies exactly one source workflow attempt and artifact name.
type Request struct {
	Repository string `json:"repository"`
	RunID      string `json:"runId"`
	Attempt    string `json:"attempt"`
	Name       string `json:"name"`
}

// Options supplies explicit credentials; ambient Docker credentials are unused.
// Username defaults to the source repository owner when empty.
type Options struct {
	Token    string
	Username string
	// RepositoryToken authenticates source repository metadata independently of
	// the package credential. When empty, Token supplies that explicit fallback.
	RepositoryToken string
	// AllowEarlierAttempts permits same-Run source attempts up to Request.Attempt
	// in DownloadGitHub and DownloadGitHubNested only. Exact store APIs and
	// locator validation remain strict; original source bindings are preserved.
	AllowEarlierAttempts bool
}

// Locator is descriptive metadata suitable for a small Actions artifact.
// Paths retain original workspace-relative roots; StripPrefix gives the upload
// action's least common ancestor. Reference authenticates the image/config bytes.
type Locator struct {
	SchemaVersion string           `json:"schemaVersion"`
	Request       Request          `json:"request"`
	Reference     string           `json:"reference"`
	ArchiveDigest string           `json:"archiveDigest"`
	ArchiveSize   int64            `json:"archiveSize"`
	Paths         []string         `json:"paths"`
	StripPrefix   string           `json:"stripPrefix"`
	Metadata      *MetadataContent `json:"metadata,omitempty"`
}

type binding struct {
	SchemaVersion string   `json:"schemaVersion"`
	Request       Request  `json:"request"`
	ArchiveDigest string   `json:"archiveDigest"`
	ArchiveSize   int64    `json:"archiveSize"`
	Paths         []string `json:"paths"`
	StripPrefix   string   `json:"stripPrefix"`
}

func (l Locator) bindingBytes() ([]byte, error) {
	return canonical.Marshal(binding{l.SchemaVersion, l.Request, l.ArchiveDigest, l.ArchiveSize, l.Paths, l.StripPrefix})
}

// DecodeLocator accepts only one bounded canonical representation. It checks
// shape, not provider origin, registry privacy, or consumer artifact authority.
func DecodeLocator(raw []byte) (Locator, error) {
	var l Locator
	if len(raw) == 0 || len(raw) > MaxMetadataRecordBytes {
		return l, e.New(e.CodeVerifyFailed, "CI locator exceeds its metadata bound")
	}
	if err := canonical.UnmarshalExact(raw, &l); err != nil {
		return Locator{}, e.Wrap(e.CodeVerifyFailed, err, "decoding CI locator")
	}
	if err := l.Validate(l.Request); err != nil {
		return Locator{}, e.Wrap(e.CodeVerifyFailed, err, "validating decoded CI locator")
	}
	return l, nil
}

// Validate rejects a locator for a different exact workflow request. Validation
// alone does not authorize network payload access; Download also checks privacy.
func (l Locator) Validate(request Request) error {
	if err := request.validate(); err != nil {
		return err
	}
	if l.Metadata != nil || l.SchemaVersion == metadataSchema {
		return l.validateMetadata(request)
	}
	if l.SchemaVersion != SchemaVersion || l.Request != request {
		return e.New(e.CodeVerifyFailed, "CI locator does not match the exact source request")
	}
	reference, err := name.NewDigest(l.Reference, name.StrictValidation)
	if err != nil || reference.Context().Name() != request.packageName() || reference.Name() != l.Reference || !validSHA256(reference.DigestStr()) {
		return e.New(e.CodeVerifyFailed, "CI locator requires its exact GHCR package and SHA-256 image reference")
	}
	if !validSHA256(l.ArchiveDigest) || l.ArchiveSize <= 0 || l.ArchiveSize > transportarchive.MaxArchiveBytes {
		return e.New(e.CodeVerifyFailed, "CI locator has invalid archive integrity metadata")
	}
	if len(l.Paths) == 0 || len(l.Paths) > 4096 || !slices.IsSorted(l.Paths) {
		return e.New(e.CodeVerifyFailed, "CI locator requires sorted bounded workspace roots")
	}
	seen := make(map[string]bool, len(l.Paths))
	for _, p := range l.Paths {
		if !safePath(p) || seen[p] {
			return e.New(e.CodeVerifyFailed, "CI locator has an unsafe or repeated workspace root")
		}
		for ancestor := path.Dir(p); ancestor != "."; ancestor = path.Dir(ancestor) {
			if seen[ancestor] {
				return e.New(e.CodeVerifyFailed, "CI locator workspace roots overlap")
			}
		}
		seen[p] = true
	}
	if l.StripPrefix != "" && !safePath(l.StripPrefix) {
		return e.New(e.CodeVerifyFailed, "CI locator has an unsafe strip prefix")
	}
	for _, p := range l.Paths {
		if l.StripPrefix != "" && p != l.StripPrefix && !strings.HasPrefix(p, l.StripPrefix+"/") {
			return e.New(e.CodeVerifyFailed, "CI locator strip prefix does not contain every root")
		}
	}
	if raw, err := canonical.Marshal(l); err != nil || len(raw) > MaxLocatorBytes {
		return e.New(e.CodeVerifyFailed, "CI locator exceeds its canonical metadata bound")
	}
	return nil
}

// ProjectedPaths returns descriptive roots after LCA stripping. A single
// directory stripped to its contents is represented as "."; callers must still
// inspect their exact expected restored file set after Download.
func (l Locator) ProjectedPaths() ([]string, error) {
	if err := l.Validate(l.Request); err != nil {
		return nil, err
	}
	result := make([]string, len(l.Paths))
	for i, p := range l.Paths {
		if p == l.StripPrefix {
			result[i] = "."
		} else if l.StripPrefix != "" {
			result[i] = strings.TrimPrefix(p, l.StripPrefix+"/")
		} else {
			result[i] = p
		}
	}
	return result, nil
}

func (r Request) packageName() string {
	return "ghcr.io/" + strings.ToLower(r.Repository) + "/ci-artifacts"
}

func (r Request) validate() error {
	if err := validateSourceRepository(r.Repository); err != nil {
		return err
	}
	for _, value := range []string{r.RunID, r.Attempt} {
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != value {
			return e.New(e.CodeUsage, "CI carrier requires canonical positive run and attempt IDs")
		}
	}
	if !safeText(r.Name, 256) || r.Name == "." || r.Name == ".." || strings.ContainsAny(r.Name, "\\/\":<>|*?") {
		return e.New(e.CodeUsage, "CI carrier requires a safe artifact name")
	}
	return nil
}

func validateSourceRepository(repository string) error {
	parts := strings.Split(repository, "/")
	u, err := url.Parse("https://github.com/" + repository)
	if err != nil || len(parts) != 2 || !safePath(repository) || u.Host != "github.com" || u.Path != "/"+repository || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(repository, "%") {
		return e.New(e.CodeUsage, "CI storage requires a canonical owner/repository source")
	}
	if _, err := name.NewRepository("ghcr.io/"+strings.ToLower(repository)+"/ci-artifacts", name.StrictValidation); err != nil {
		return e.New(e.CodeUsage, "CI storage source is not compatible with its GHCR namespace")
	}
	return nil
}

func safeText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func safePath(p string) bool {
	return safeText(p, 4096) && securepath.WorkspaceRelative(p) && path.Clean(p) == p && !strings.Contains(p, "\\")
}

func validSHA256(value string) bool {
	h, err := v1.NewHash(value)
	return err == nil && h.Algorithm == "sha256" && h.String() == value && strings.ToLower(h.Hex) == h.Hex
}
