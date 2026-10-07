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
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/releaseasset"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// ToolSource declares an upstream release executable, without byte identities.
// URL accepts the same Tag/Version templates as other catalog acquisitions.
type ToolSource struct {
	Format         string `json:"format"`
	URL            string `json:"url"`
	Platform       string `json:"platform"`
	ExecutablePath string `json:"executablePath"`
}

// ToolPin binds the release asset and executable bytes to the enclosing source pin.
type ToolPin struct {
	Format           string `json:"format"`
	URL              string `json:"url"`
	Platform         string `json:"platform"`
	ExecutablePath   string `json:"executablePath"`
	ArchiveDigest    string `json:"archiveDigest"`
	ExecutableDigest string `json:"executableDigest"`
}

const maxToolArchiveBytes = releaseasset.MaxToolTransportBytes

func toolArchiveLimits() releaseasset.Limits {
	return releaseasset.ToolLimits()
}

func validateToolSource(tool ToolSource) error {
	if tool.Format != "raw" && tool.Format != "tar-gzip" {
		return e.New(e.CodeInvalidCatalog, "unsupported tool format %q", tool.Format)
	}
	if tool.Platform != "linux/amd64" && tool.Platform != "linux/arm64" {
		return e.New(e.CodeInvalidCatalog, "tool requires an explicit supported platform")
	}
	if !safeToolPath(tool.ExecutablePath) {
		return e.New(e.CodeInvalidCatalog, "unsafe tool executable path %q", tool.ExecutablePath)
	}
	rendered, err := renderRef("tool URL", tool.URL, Pin{Tag: "v1.2.3", Commit: strings.Repeat("a", 40)})
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "invalid tool URL template")
	}
	return validateToolURL(rendered)
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
	if err := validateToolSource(ToolSource{Format: pin.Format, URL: pin.URL, Platform: pin.Platform, ExecutablePath: pin.ExecutablePath}); err != nil {
		return err
	}
	if !canonical.IsDigest(pin.ArchiveDigest) || !canonical.IsDigest(pin.ExecutableDigest) || (pin.Format == "raw" && pin.ArchiveDigest != pin.ExecutableDigest) {
		return e.New(e.CodeUnpinnedSource, "tool requires exact archive and executable digests")
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
	if pin.Tool.URL != expected || pin.Tool.Format != source.Format || pin.Tool.Platform != source.Platform || pin.Tool.ExecutablePath != source.ExecutablePath {
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
	if source.Format == "tar-gzip" {
		member, err := releaseasset.SelectTarGzip(ctx, bytes.NewReader(raw), source.ExecutablePath, toolArchiveLimits())
		if err != nil {
			return ToolPin{}, err
		}
		executableDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(member))
	}
	return ToolPin{Format: source.Format, URL: location, Platform: source.Platform, ExecutablePath: source.ExecutablePath, ArchiveDigest: archiveDigest, ExecutableDigest: executableDigest}, nil
}
