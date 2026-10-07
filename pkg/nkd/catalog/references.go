// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"regexp"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	ociname "github.com/google/go-containerregistry/pkg/name"
)

var (
	sourceRepoSchemes = map[string]bool{"https": true, "git": true, "ssh": true}
	scpLikeRepo       = regexp.MustCompile(`^[^@\s/]+@[^:\s/]+:[^\s]+$`)
)

// uriScheme returns the lowercased scheme of a "scheme://…" reference, or "" for a
// scp-like (git@host:path) or bare reference that carries no explicit scheme.
func uriScheme(ref string) string {
	if i := strings.Index(ref, "://"); i > 0 {
		return strings.ToLower(ref[:i])
	}
	return ""
}

// checkSourceRepoScheme allows https/git/ssh and scp-like git@host:path (no scheme).
func checkSourceRepoScheme(name, repo string) error {
	if s := uriScheme(repo); s != "" {
		if sourceRepoSchemes[s] {
			return nil
		}
		return e.New(e.CodeInvalidCatalog,
			"component %q: source repo scheme %q is not allowed (use https, git, or ssh)", name, s)
	}
	if scpLikeRepo.MatchString(repo) {
		return nil
	}
	return e.New(e.CodeInvalidCatalog,
		"component %q: source repo %q is not an https/git/ssh or scp-like SSH reference", name, repo)
}

// checkPrebuiltURLScheme requires https for a prebuilt binary download.
func checkPrebuiltURLScheme(name, url string) error {
	if s := uriScheme(url); s != "https" {
		got := s
		if got == "" {
			got = "(none)"
		}
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: download url scheme %q is not allowed (https only)", name, got)
	}
	return nil
}

// validateFlake fails closed unless a nix builder's flake is fully pinned (ADR-10 §2):
// a flake ref, a lock digest over the transitive closure, an output attribute, and (if
// present) an OCI-scheme nix image. A tag-pinned or unpinned nix image fails closed.
func validateFlake(name string, f *FlakePin) error {
	if f == nil {
		return e.New(e.CodeUnpinnedSource,
			"nix component %q: builderType nix requires a pinned flake", name)
	}
	if strings.TrimSpace(f.Ref) == "" {
		return e.New(e.CodeUnpinnedSource, "nix component %q: flake.ref is empty", name)
	}
	if !canonical.IsDigest(f.LockDigest) {
		return e.New(e.CodeUnpinnedSource,
			"nix component %q: flake.lockDigest %q is not a bare sha256:<64-hex> closure pin", name, f.LockDigest)
	}
	if strings.TrimSpace(f.Output) == "" {
		return e.New(e.CodeInvalidCatalog, "nix component %q: flake.output is empty", name)
	}
	if f.Image != "" {
		if err := checkOCIRefScheme(name, "flake.image", f.Image); err != nil {
			return err
		}
		if !digestPinned.MatchString(f.Image) {
			return e.New(e.CodeTagToolchain,
				"nix component %q: flake.image %q is not pinned by @sha256: digest", name, f.Image)
		}
	}
	return nil
}

// checkOCIRefScheme allows a bare OCI reference (no scheme) or an explicit oci://.
func checkOCIRefScheme(name, field, ref string) error {
	if ref == "" {
		return nil // the caller emits the field-specific missing-pin error
	}
	if s := uriScheme(ref); s != "" && s != "oci" {
		return e.New(e.CodeInvalidCatalog,
			"component %q: %s ref scheme %q is not allowed (use a bare OCI ref or oci://)", name, field, s)
	}
	normalized := strings.TrimPrefix(ref, "oci://")
	if canonical.IsDigest(normalized) {
		return nil // explicitly supported local content-addressed image ID
	}
	if _, err := ociname.ParseReference(normalized); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err,
			"component %q: %s %q is not a well-formed OCI reference", name, field, ref)
	}
	return nil
}

// checkOCIRepository validates the tag-less repository base used to construct
// `<base>:<resolved-go-version>`. Accepting a tag or digest here would make that
// construction ambiguous (or syntactically invalid) and create two version inputs.
func checkOCIRepository(name, field, ref string) error {
	if err := checkOCIRefScheme(name, field, ref); err != nil {
		return err
	}
	normalized := strings.TrimPrefix(ref, "oci://")
	if _, err := ociname.NewRepository(normalized); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err,
			"component %q: %s %q must be a tag-less OCI repository", name, field, ref)
	}
	return nil
}
