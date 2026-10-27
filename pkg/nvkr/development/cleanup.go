// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package development

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	cleanupMinimumAge         = 24 * time.Hour
	cleanupTimeout            = 10 * time.Minute
	cleanupBodyLimit          = 8 << 20
	cleanupPageLimit          = 100
	cleanupPageSize           = 100
	cleanupCanceledConclusion = "cancelled" //nolint:misspell // Exact GitHub API conclusion spelling.
)

// CleanupReport records API-confirmed package deletion, not physical registry
// garbage collection. Candidates include all versions, signatures and referrers
// in each package; the janitor never guesses reachability from mutable tags.
type CleanupReport struct {
	Repository string   `json:"repository"`
	Apply      bool     `json:"apply"`
	Candidates []string `json:"candidates"`
	Deleted    []string `json:"deleted"`
}

// Cleanup expires only internal development packages owned by completed, expired
// nvkr-dev.yml attempts. Credentials are explicit and never read from ambient
// GitHub or Docker configuration. The namespace is reserved for these attempts;
// a later attempt must publish into its own namespace, never recreate an old one.
// A failed call may return a report of deletions already acknowledged by GitHub.
func Cleanup(ctx context.Context, repository, token string, apply bool) (CleanupReport, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return e.New(e.CodeFetchFailed, "GitHub cleanup API redirects are not admitted")
		},
	}
	return (&cleanupAPI{client: client, baseURL: "https://api.github.com", now: time.Now}).cleanup(ctx, repository, token, apply)
}

type cleanupAPI struct {
	client  *http.Client
	baseURL string
	now     func() time.Time
}

type cleanupAccount struct {
	ID    uint64 `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type cleanupRepository struct {
	ID       uint64         `json:"id"`
	FullName string         `json:"full_name"`
	Owner    cleanupAccount `json:"owner"`
}

type cleanupPackage struct {
	ID         uint64         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"package_type"`
	Visibility string         `json:"visibility"`
	Owner      cleanupAccount `json:"owner"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type cleanupVersion struct {
	ID        uint64    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type cleanupRun struct {
	ID         uint64            `json:"id"`
	Attempt    uint64            `json:"run_attempt"`
	Path       string            `json:"path"`
	Event      string            `json:"event"`
	Status     string            `json:"status"`
	Conclusion string            `json:"conclusion"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Repository cleanupRepository `json:"repository"`
}

type cleanupCandidate struct {
	metadata  cleanupPackage
	reference Reference
	path      string
}

func (api *cleanupAPI) cleanup(ctx context.Context, repository, token string, apply bool) (CleanupReport, error) {
	repository = strings.ToLower(repository)
	report := CleanupReport{Repository: repository, Apply: apply, Candidates: []string{}, Deleted: []string{}}
	namespace, err := ParseNamespace("ghcr.io/" + repository + "/development")
	if err != nil {
		return report, err
	}
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n\x00") {
		return report, e.New(e.CodeTrustPolicyMissing, "development cleanup requires the dedicated NVKR_DEV_REGISTRY_CLEANUP credential")
	}
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	var repo cleanupRepository
	if err := api.request(ctx, token, http.MethodGet, "/repos/"+repository, &repo); err != nil {
		return report, err
	}
	owner, _, _ := strings.Cut(repository, "/")
	if repo.ID == 0 || !strings.EqualFold(repo.FullName, repository) || repo.Owner.ID == 0 || !strings.EqualFold(repo.Owner.Login, owner) {
		return report, e.New(e.CodeVerifyFailed, "GitHub cleanup repository identity does not match %s", repository)
	}
	var accountPath string
	switch repo.Owner.Type {
	case "Organization":
		accountPath = "/orgs/" + owner + "/packages"
	case "User":
		accountPath = "/users/" + owner + "/packages"
	default:
		return report, e.New(e.CodeVerifyFailed, "GitHub cleanup repository has an unsupported owner type")
	}
	packages, err := api.packages(ctx, token, accountPath)
	if err != nil {
		return report, err
	}
	// Fully inventory and validate every candidate before the first mutation.
	var candidates []cleanupCandidate
	packagePrefix := strings.TrimPrefix(namespace.Prefix(), "ghcr.io/"+owner+"/") + "/"
	for _, pkg := range packages {
		if !strings.HasPrefix(pkg.Name, packagePrefix) {
			continue
		}
		ref, err := namespace.ParseReference("ghcr.io/" + owner + "/" + pkg.Name)
		if err != nil {
			return report, err
		}
		candidate := cleanupCandidate{metadata: pkg, reference: ref, path: accountPath + "/container/" + url.PathEscape(pkg.Name)}
		eligible, err := api.eligible(ctx, token, repo, candidate, false)
		if err != nil {
			return report, err
		}
		if eligible {
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].metadata.Name < candidates[j].metadata.Name })
	for _, candidate := range candidates {
		report.Candidates = append(report.Candidates, candidate.reference.String())
	}
	if !apply {
		return report, nil
	}
	for _, candidate := range candidates {
		eligible, err := api.eligible(ctx, token, repo, candidate, true)
		if err != nil {
			return report, err
		}
		if !eligible {
			return report, e.New(e.CodeVerifyFailed, "development package %s changed eligibility before deletion", candidate.metadata.Name)
		}
		// No retry: a transport failure after DELETE has an unknown outcome.
		if err := api.request(ctx, token, http.MethodDelete, candidate.path, nil); err != nil {
			return report, err
		}
		report.Deleted = append(report.Deleted, candidate.reference.String())
	}
	return report, nil
}

func (api *cleanupAPI) packages(ctx context.Context, token, path string) ([]cleanupPackage, error) {
	var all []cleanupPackage
	ids, names := map[uint64]bool{}, map[string]bool{}
	for page := 1; page <= cleanupPageLimit; page++ {
		var items []cleanupPackage
		if err := api.request(ctx, token, http.MethodGet, fmt.Sprintf("%s?package_type=container&per_page=%d&page=%d", path, cleanupPageSize, page), &items); err != nil {
			return nil, err
		}
		if items == nil || len(items) > cleanupPageSize {
			return nil, e.New(e.CodeVerifyFailed, "GitHub package inventory is malformed")
		}
		for _, item := range items {
			if item.ID == 0 || item.Name == "" || ids[item.ID] || names[item.Name] {
				return nil, e.New(e.CodeVerifyFailed, "GitHub package inventory contains missing or duplicate identities")
			}
			ids[item.ID], names[item.Name] = true, true
		}
		all = append(all, items...)
		if len(items) < cleanupPageSize {
			return all, nil
		}
	}
	return nil, e.New(e.CodeVerifyFailed, "GitHub package inventory exceeds the bounded cleanup page limit")
}

func (api *cleanupAPI) eligible(ctx context.Context, token string, repo cleanupRepository, candidate cleanupCandidate, recheck bool) (bool, error) {
	var pkg cleanupPackage
	if recheck {
		if err := api.request(ctx, token, http.MethodGet, candidate.path, &pkg); err != nil {
			return false, err
		}
	} else {
		pkg = candidate.metadata
	}
	if pkg.ID != candidate.metadata.ID || pkg.Name != candidate.metadata.Name || pkg.Type != "container" || pkg.Visibility != "internal" ||
		pkg.Owner.ID != repo.Owner.ID || !strings.EqualFold(pkg.Owner.Login, repo.Owner.Login) || pkg.Owner.Type != repo.Owner.Type {
		return false, e.New(e.CodeVerifyFailed, "development package ownership, internal visibility or identity does not match its admitted namespace")
	}
	old, err := api.oldEnough(pkg.CreatedAt, pkg.UpdatedAt)
	if err != nil || !old {
		return false, err
	}
	versionsOld, err := api.versionsOldEnough(ctx, token, candidate.path)
	if err != nil || !versionsOld {
		return false, err
	}
	// Authenticate ownership through the original attempt, then check the latest
	// attempt before DELETE: a retry may still consume these original packages.
	var run cleanupRun
	path := "/repos/" + repo.FullName + "/actions/runs/" + candidate.reference.RunID() + "/attempts/" + candidate.reference.Attempt()
	if err := api.request(ctx, token, http.MethodGet, path, &run); err != nil {
		return false, err
	}
	if strconv.FormatUint(run.ID, 10) != candidate.reference.RunID() || strconv.FormatUint(run.Attempt, 10) != candidate.reference.Attempt() ||
		run.Repository.ID != repo.ID || !strings.EqualFold(run.Repository.FullName, repo.FullName) ||
		run.Path != ".github/workflows/nvkr-dev.yml" || run.Event != "workflow_dispatch" {
		return false, e.New(e.CodeVerifyFailed, "development package does not bind the expected repository, workflow and exact attempt")
	}
	old, err = api.runOldEnough(run)
	if err != nil || !old {
		return false, err
	}
	var latest cleanupRun
	if err := api.request(ctx, token, http.MethodGet, "/repos/"+repo.FullName+"/actions/runs/"+candidate.reference.RunID(), &latest); err != nil {
		return false, err
	}
	if latest.ID != run.ID || latest.Attempt < run.Attempt || latest.Repository.ID != repo.ID ||
		!strings.EqualFold(latest.Repository.FullName, repo.FullName) || latest.Path != run.Path || latest.Event != run.Event {
		return false, e.New(e.CodeVerifyFailed, "development cleanup latest run differs from the original owner")
	}
	return api.runOldEnough(latest)
}

func (api *cleanupAPI) runOldEnough(run cleanupRun) (bool, error) {
	old, err := api.oldEnough(run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return false, err
	}
	switch run.Status {
	case "queued", "in_progress", "requested", "waiting", "pending":
		return false, nil
	case "completed":
		switch run.Conclusion {
		case "success", "failure", "neutral", cleanupCanceledConclusion, "skipped", "timed_out", "action_required", "stale", "startup_failure":
			return old, nil
		}
	}
	return false, e.New(e.CodeVerifyFailed, "development cleanup received an unknown workflow attempt state")
}

func (api *cleanupAPI) versionsOldEnough(ctx context.Context, token, path string) (bool, error) {
	ids := map[uint64]bool{}
	allOld := true
	for page := 1; page <= cleanupPageLimit; page++ {
		var versions []cleanupVersion
		if err := api.request(ctx, token, http.MethodGet, fmt.Sprintf("%s/versions?state=active&per_page=%d&page=%d", path, cleanupPageSize, page), &versions); err != nil {
			return false, err
		}
		if versions == nil || len(versions) > cleanupPageSize {
			return false, e.New(e.CodeVerifyFailed, "GitHub package version inventory is malformed")
		}
		for _, version := range versions {
			if version.ID == 0 || ids[version.ID] {
				return false, e.New(e.CodeVerifyFailed, "GitHub package versions contain missing or duplicate identities")
			}
			ids[version.ID] = true
			old, err := api.oldEnough(version.CreatedAt, version.UpdatedAt)
			if err != nil {
				return false, err
			}
			allOld = allOld && old
		}
		if len(versions) < cleanupPageSize {
			return allOld, nil
		}
	}
	return false, e.New(e.CodeVerifyFailed, "GitHub package versions exceed the bounded cleanup page limit")
}

func (api *cleanupAPI) oldEnough(created, updated time.Time) (bool, error) {
	now := api.now()
	if created.IsZero() || updated.IsZero() || updated.Before(created) || updated.After(now) {
		return false, e.New(e.CodeVerifyFailed, "GitHub cleanup received missing or inconsistent timestamps")
	}
	return !updated.After(now.Add(-cleanupMinimumAge)), nil
}

func (api *cleanupAPI) request(ctx context.Context, token, method, path string, target any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, api.baseURL+path, nil)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "constructing development cleanup API request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := api.client.Do(req)
	if err != nil {
		// Do not include upstream errors/response bodies, which may contain
		// credentials. A failed mutation is explicitly an unknown outcome.
		if method == http.MethodDelete {
			return e.New(e.CodeFetchFailed, "development package deletion outcome is unknown; inspect the package before retrying cleanup")
		}
		if ctx.Err() != nil {
			return e.New(e.CodeTimeout, "development cleanup API request was canceled or timed out")
		}
		return e.New(e.CodeFetchFailed, "development cleanup API request failed")
	}
	defer response.Body.Close()
	want := http.StatusOK
	if method == http.MethodDelete {
		want = http.StatusNoContent
	}
	if response.StatusCode != want {
		if method == http.MethodDelete {
			return e.New(e.CodeFetchFailed, "development package deletion was not confirmed (HTTP %d); inspect the package before retrying cleanup", response.StatusCode)
		}
		return e.New(e.CodeFetchFailed, "development cleanup API %s returned HTTP %d; no automatic retry", method, response.StatusCode)
	}
	if target == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, cleanupBodyLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return e.New(e.CodeTimeout, "reading development cleanup API response was canceled or timed out")
		}
		return e.New(e.CodeFetchFailed, "reading development cleanup API response failed")
	}
	if len(data) > cleanupBodyLimit {
		return e.New(e.CodeVerifyFailed, "development cleanup API response exceeds its size limit")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return e.New(e.CodeVerifyFailed, "development cleanup API response is malformed")
	}
	return nil
}
