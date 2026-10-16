// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type backend struct {
	api       string
	client    *http.Client
	transport http.RoundTripper
}

func newBackend() *backend {
	return &backend{
		api: "https://api.github.com",
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return e.New(e.CodeFetchFailed, "CI storage API redirects are not admitted")
		}},
		transport: http.DefaultTransport,
	}
}

type account struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type repositoryRecord struct {
	ID         uint64          `json:"id"`
	FullName   string          `json:"full_name"`
	Owner      account         `json:"owner"`
	Private    *bool           `json:"private"`
	Visibility json.RawMessage `json:"visibility"`
}

type packageRecord struct {
	Name        string          `json:"name"`
	PackageType string          `json:"package_type"`
	Visibility  string          `json:"visibility"`
	Owner       account         `json:"owner"`
	Repository  json.RawMessage `json:"repository"`
}

func (b *backend) source(ctx context.Context, repository string, opts Options) (repositoryRecord, error) {
	var record repositoryRecord
	token := opts.RepositoryToken
	if token == "" {
		token = opts.Token
	}
	status, err := b.get(ctx, "/repos/"+repository, token, &record)
	if err != nil {
		return record, err
	}
	if status != http.StatusOK || record.ID == 0 || record.FullName != repository || record.Owner.Login != strings.Split(repository, "/")[0] || (record.Owner.Type != "Organization" && record.Owner.Type != "User") {
		return record, e.New(e.CodeTrustPolicyMissing, "CI storage source repository identity was not authenticated by GitHub")
	}
	return record, nil
}

// Transfer packages must have no reported repository association even while the
// source repository is private. Publication separately permits private linkage.
// Neither check proves independent grants; operators retain that ACL obligation.
func (b *backend) checkPrivacy(ctx context.Context, request Request, source repositoryRecord, opts Options) (bool, error) {
	return b.checkPackagePrivacy(ctx, strings.ToLower(strings.Split(request.Repository, "/")[1])+"/ci-artifacts", source, opts, true)
}

func (b *backend) checkPackagePrivacy(ctx context.Context, packageName string, source repositoryRecord, opts Options, requireUnlinked bool) (bool, error) {
	kind := "users"
	if source.Owner.Type == "Organization" {
		kind = "orgs"
	}
	packagePath := "/" + kind + "/" + url.PathEscape(source.Owner.Login) + "/packages/container/" + url.PathEscape(packageName)
	var record packageRecord
	status, err := b.get(ctx, packagePath, opts.Token, &record)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if status != http.StatusOK {
		return false, e.New(e.CodeTrustPolicyMissing, "CI storage package access could not be authenticated")
	}
	if record.Name != packageName || record.PackageType != "container" || !strings.EqualFold(record.Owner.Login, source.Owner.Login) || !privateVisibility(record.Visibility) {
		return false, e.New(e.CodeTrustPolicyMissing, "CI storage requires the exact private or internal GHCR package")
	}
	// GitHub's package schema makes repository optional and nullable; its GET
	// example omits the field. Both forms report no association. This is only
	// provider metadata, not proof of effective grants or future visibility.
	// https://docs.github.com/en/rest/packages/packages#get-a-package-for-an-organization
	if len(record.Repository) != 0 && !bytes.Equal(bytes.TrimSpace(record.Repository), []byte("null")) {
		if requireUnlinked {
			return false, e.New(e.CodeTrustPolicyMissing, "CI transfer requires an unlinked private/internal package")
		}
		var linked repositoryRecord
		if err := json.Unmarshal(record.Repository, &linked); err != nil || linked.ID == 0 || linked.FullName == "" || linked.Private == nil || !*linked.Private || !privateRepositoryVisibility(linked.Visibility) {
			return false, e.New(e.CodeTrustPolicyMissing, "CI storage rejects public or unknown linked repository access; a public source GITHUB_TOKEN may auto-link an unsuitable package")
		}
	}
	return true, nil
}

func (b *backend) ensurePrivatePackage(ctx context.Context, repository name.Repository, source repositoryRecord, opts Options, requireUnlinked bool) error {
	// The repository was admitted before this operation; GitHub names packages
	// relative to the already-authenticated owner namespace.
	packageName := strings.TrimPrefix(repository.RepositoryStr(), strings.ToLower(source.Owner.Login)+"/")
	exists, err := b.checkPackagePrivacy(ctx, packageName, source, opts, requireUnlinked)
	if err != nil || exists {
		return err
	}
	// This is the only pre-admission write: no filesystem layers, payload,
	// source path or run metadata. Never rely on GHCR's initial visibility.
	seed, err := mutate.Config(empty.Image, v1.Config{Labels: map[string]string{"nvkr.dev/ci-carrier-seed": "metadata-only-v1"}, Cmd: []string{"/__nvkr_ci_carrier_not_executable__"}})
	if err != nil {
		return e.New(e.CodeInternal, "constructing metadata-only private package seed")
	}
	if err := remote.Write(repository.Tag("privacy-probe-v1"), seed, b.registryOptions(ctx, opts)...); err != nil {
		return safeIOError(ctx, e.CodeFetchFailed, "seeding metadata-only private package")
	}
	exists, err = b.checkPackagePrivacy(ctx, packageName, source, opts, requireUnlinked)
	if err != nil {
		return err
	}
	if !exists {
		return e.New(e.CodeTrustPolicyMissing, "metadata seed did not establish an API-confirmed private package")
	}
	return nil
}

func privateVisibility(visibility string) bool {
	return visibility == "private" || visibility == "internal"
}

// GitHub's minimal repository schema requires private, checked by the caller,
// but permits visibility to be omitted. A supplied value must still be known
// and private/internal; explicit null, empty, public and malformed values fail.
func privateRepositoryVisibility(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var visibility string
	return json.Unmarshal(raw, &visibility) == nil && privateVisibility(visibility)
}

func (b *backend) get(ctx context.Context, endpoint, token string, result any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api+endpoint, nil)
	if err != nil {
		return 0, e.New(e.CodeFetchFailed, "constructing CI storage admission request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := b.client.Do(req)
	if err != nil {
		return 0, safeIOError(ctx, e.CodeFetchFailed, "CI storage admission request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, nil
	}
	const bodyLimit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, bodyLimit+1))
	if err != nil {
		return 0, safeIOError(ctx, e.CodeFetchFailed, "reading CI storage admission response")
	}
	if len(raw) > bodyLimit || canonical.ValidateJSONDocument(raw) != nil || json.Unmarshal(raw, result) != nil {
		return 0, e.New(e.CodeTrustPolicyMissing, "CI storage admission response is malformed or oversized")
	}
	return response.StatusCode, nil
}

func safeIOError(ctx context.Context, code e.Code, message string) error {
	if ctx != nil && ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s", message)
	}
	// Upstream HTTP and registry errors can contain credentials or response bodies.
	return e.New(code, "%s", message)
}

func admit(ctx context.Context, request Request, opts Options) (context.Context, context.CancelFunc, Options, error) {
	if ctx == nil {
		return nil, nil, opts, e.New(e.CodeUsage, "CI storage requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, opts, e.Wrap(e.CodeTimeout, err, "CI storage canceled")
	}
	if err := request.validate(); err != nil {
		return nil, nil, opts, err
	}
	return admitSource(ctx, request.Repository, opts)
}

func admitSource(ctx context.Context, repository string, opts Options) (context.Context, context.CancelFunc, Options, error) {
	if ctx == nil {
		return nil, nil, opts, e.New(e.CodeUsage, "private package admission requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, opts, e.Wrap(e.CodeTimeout, err, "private package admission canceled")
	}
	if err := validateSourceRepository(repository); err != nil {
		return nil, nil, opts, err
	}
	if opts.RepositoryToken != "" && !safeText(opts.RepositoryToken, 16384) {
		return nil, nil, opts, e.New(e.CodeTrustPolicyMissing, "repository metadata credential is malformed")
	}
	if !safeText(opts.Token, 16384) {
		return nil, nil, opts, e.New(e.CodeTrustPolicyMissing, "CI storage requires an explicit access token")
	}
	if opts.Username == "" {
		opts.Username = strings.Split(repository, "/")[0]
	}
	if !safeText(opts.Username, 256) || strings.Contains(opts.Username, ":") {
		return nil, nil, opts, e.New(e.CodeUsage, "CI storage requires a safe credential username")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	return ctx, cancel, opts, nil
}
