// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/releaseasset"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// ToolSource declares an upstream release executable, without byte identities.
// URL accepts the same Tag/Version templates as other catalog acquisitions.
// Companions name further archive members delivered beside the executable.
type ToolSource struct {
	Format         string   `json:"format"`
	URL            string   `json:"url"`
	Platform       string   `json:"platform"`
	ExecutablePath string   `json:"executablePath"`
	Companions     []string `json:"companions,omitempty"`
}

// ToolPin binds the release asset and executable bytes to the enclosing source pin.
type ToolPin struct {
	Format           string          `json:"format"`
	URL              string          `json:"url"`
	Platform         string          `json:"platform"`
	ExecutablePath   string          `json:"executablePath"`
	ArchiveDigest    string          `json:"archiveDigest"`
	ExecutableDigest string          `json:"executableDigest"`
	Companions       []ToolCompanion `json:"companions,omitempty"`
}

// ToolCompanion pins one non-executable archive member selected beside the executable.
type ToolCompanion struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

func (p ToolPin) clone() ToolPin {
	p.Companions = slices.Clone(p.Companions)
	return p
}

func (p ToolPin) companionPaths() []string {
	paths := make([]string, 0, len(p.Companions))
	for _, companion := range p.Companions {
		paths = append(paths, companion.Path)
	}
	return paths
}

const maxToolArchiveBytes = releaseasset.MaxToolTransportBytes

func toolArchiveLimits() releaseasset.Limits {
	return releaseasset.ToolLimits()
}

func validateToolSource(tool ToolSource) error {
	if tool.Format != "raw" && tool.Format != "tar-gzip" && tool.Format != "zip" {
		return e.New(e.CodeInvalidCatalog, "unsupported tool format %q", tool.Format)
	}
	if err := target.ValidateExecutablePlatform(tool.Platform); err != nil {
		return err
	}
	if !safeToolPath(tool.ExecutablePath) {
		return e.New(e.CodeInvalidCatalog, "unsafe tool executable path %q", tool.ExecutablePath)
	}
	if err := validateToolCompanions(tool); err != nil {
		return err
	}
	rendered, err := renderRef("tool URL", tool.URL, Pin{Tag: "v1.2.3", Commit: strings.Repeat("a", 40)})
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "invalid tool URL template")
	}
	return validateToolURL(rendered)
}

// Companions land beside the executable under their base names, so names must
// be exact, sorted and distinct from each other and from the executable.
func validateToolCompanions(tool ToolSource) error {
	if len(tool.Companions) == 0 {
		return nil
	}
	if tool.Format == "raw" {
		return e.New(e.CodeInvalidCatalog, "raw tool asset has no archive members for companions")
	}
	names := map[string]bool{path.Base(tool.ExecutablePath): true}
	for i, companion := range tool.Companions {
		if !safeToolPath(companion) {
			return e.New(e.CodeInvalidCatalog, "unsafe tool companion path %q", companion)
		}
		if i > 0 && tool.Companions[i-1] >= companion {
			return e.New(e.CodeInvalidCatalog, "tool companions must be sorted and unique")
		}
		if names[path.Base(companion)] {
			return e.New(e.CodeInvalidCatalog, "tool companion %q collides with another delivered name", companion)
		}
		names[path.Base(companion)] = true
	}
	return nil
}
func validateToolURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "{}\r\n") {
		return e.New(e.CodeInvalidCatalog, "tool URL must be canonical HTTPS without credentials, query or fragment")
	}
	return nil
}
func safeToolPath(p string) bool {
	return p != "" && p != "." && p != ".." && !strings.HasPrefix(p, "../") && !path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\\\x00")
}
func validateToolPin(pin ToolPin) error {
	if err := validateToolSource(ToolSource{Format: pin.Format, URL: pin.URL, Platform: pin.Platform, ExecutablePath: pin.ExecutablePath,
		Companions: pin.companionPaths()}); err != nil {
		return err
	}
	if !canonical.IsDigest(pin.ArchiveDigest) || !canonical.IsDigest(pin.ExecutableDigest) || (pin.Format == "raw" && pin.ArchiveDigest != pin.ExecutableDigest) {
		return e.New(e.CodeUnpinnedSource, "tool requires exact archive and executable digests")
	}
	for _, companion := range pin.Companions {
		if !canonical.IsDigest(companion.Digest) {
			return e.New(e.CodeUnpinnedSource, "tool companion %q requires an exact digest", companion.Path)
		}
	}
	return nil
}
func checkToolPin(source *ToolSource, pin Pin) error {
	if source == nil {
		if pin.Tool != nil {
			return e.New(e.CodeInvalidCatalog, "orphan tool pin")
		}
		return nil
	}
	if pin.Tool == nil {
		return e.New(e.CodeUnpinnedSource, "tool source %q requires release asset pinning", pin.Tag)
	}
	expected, err := renderRef("tool URL", source.URL, pin)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "rendering pinned tool URL")
	}
	if pin.Tool.URL != expected || pin.Tool.Format != source.Format || pin.Tool.Platform != source.Platform || pin.Tool.ExecutablePath != source.ExecutablePath ||
		!slices.Equal(pin.Tool.companionPaths(), source.Companions) {
		return e.New(e.CodeUnpinnedSource, "tool pin does not match source declaration and version")
	}
	return validateToolPin(*pin.Tool)
}
func resolveToolPin(ctx context.Context, source ToolSource, pin Pin) (ToolPin, error) {
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return e.New(e.CodeFetchFailed, "unsafe tool redirect")
		}
		return nil
	}}
	return resolveToolWithClient(ctx, source, pin, client)
}
func resolveToolWithClient(ctx context.Context, source ToolSource, pin Pin, client *http.Client) (ToolPin, error) {
	if err := pinContextError(ctx); err != nil {
		return ToolPin{}, err
	}
	if err := validateToolSource(source); err != nil {
		return ToolPin{}, err
	}
	location, err := renderRef("tool URL", source.URL, pin)
	if err != nil {
		return ToolPin{}, e.Wrap(e.CodeInvalidCatalog, err, "tool URL")
	}
	if err := validateToolURL(location); err != nil {
		return ToolPin{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return ToolPin{}, e.Wrap(e.CodeInvalidCatalog, err, "tool request")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ToolPin{}, pinContextError(ctx)
		}
		return ToolPin{}, e.Wrap(e.CodeFetchFailed, err, "fetching tool")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ToolPin{}, e.New(e.CodeFetchFailed, "tool download returned HTTP %d", response.StatusCode)
	}
	const limit = maxToolArchiveBytes
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return ToolPin{}, pinContextError(ctx)
		}
		return ToolPin{}, e.Wrap(e.CodeFetchFailed, err, "reading tool")
	}
	if len(raw) == 0 || len(raw) > limit {
		return ToolPin{}, e.New(e.CodeVerifyFailed, "tool archive is empty or exceeds byte limit")
	}
	archiveDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	executableDigest := archiveDigest
	if source.Format != "raw" {
		member, err := releaseasset.Select(ctx, bytes.NewReader(raw), int64(len(raw)), source.Format, source.ExecutablePath, toolArchiveLimits())
		if err != nil {
			return ToolPin{}, err
		}
		executableDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(member))
	}
	var companions []ToolCompanion
	for _, companion := range source.Companions {
		member, err := releaseasset.Select(ctx, bytes.NewReader(raw), int64(len(raw)), source.Format, companion, toolArchiveLimits())
		if err != nil {
			return ToolPin{}, err
		}
		companions = append(companions, ToolCompanion{Path: companion, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(member))})
	}
	return ToolPin{Format: source.Format, URL: location, Platform: source.Platform, ExecutablePath: source.ExecutablePath,
		ArchiveDigest: archiveDigest, ExecutableDigest: executableDigest, Companions: companions}, nil
}
