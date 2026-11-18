// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package gitlab authenticates the current GitLab job and its current job map.
// It does not grant artifact, publication, signing or qualification authority.
package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	maxPageBytes = 4 << 20
	pageSize     = 100
	maxPages     = 100
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Config names one explicitly selected GitLab API and scoped credentials.
type Config struct {
	APIURL             string
	ProjectID          uint64
	JobToken           string
	ReadCredential     string
	ReadCredentialKind string
}

// Identity describes the provider invocation; Ref and Source are authenticated metadata.
type Identity struct {
	APIURL     string
	ProjectID  uint64
	PipelineID uint64
	JobID      uint64
	Commit     string
	Ref        string
	Source     string
}

// Job is one provider-reported current job observation.
type Job struct {
	ID     uint64
	Name   string
	Status string
}

// Client owns the bounded native provider lookup and its credentials.
type Client struct {
	mu                                 sync.Mutex
	wg                                 sync.WaitGroup
	closed                             bool
	lifetime                           context.Context
	cancel                             context.CancelFunc
	base                               *url.URL
	project                            uint64
	jobToken, readCredential, readKind string
	http                               *http.Client
	waitRetry                          func(context.Context, time.Duration) error
}

// Receipt holds authenticated observations, not a server-atomic snapshot or
// durable retry authority.
type Receipt struct {
	identity  Identity
	jobs      map[string]Job
	registry  ProjectRegistry
	startedAt time.Time
}

// StartedAt returns the authenticated current job's provider-reported start.
// Zero means it was absent; execution-budget owners must refuse that case.
func (r *Receipt) StartedAt() time.Time {
	if r == nil {
		return time.Time{}
	}
	return r.startedAt
}

// ProjectRegistry is provider-reported metadata, not an independent authority.
// Present distinguishes an omitted or null field from a reported disabled registry.
type ProjectRegistry struct {
	Present bool
	Enabled bool
	Prefix  string
}

// ProjectRegistry returns the authenticated project metadata observed with this map.
func (r *Receipt) ProjectRegistry() ProjectRegistry {
	if r == nil {
		return ProjectRegistry{}
	}
	return r.registry
}

// CurrentIdentity returns a copy of the authenticated invocation description.
func (r *Receipt) CurrentIdentity() Identity {
	if r == nil {
		return Identity{}
	}
	return r.identity
}

// Job returns a copy of the current-map observation for an exact generated name.
func (r *Receipt) Job(name string) (Job, bool) {
	if r == nil {
		return Job{}, false
	}
	j, ok := r.jobs[name]
	return j, ok
}

// New admits one HTTPS API scope and explicit job/read credentials. The owned
// client lifetime derives from the caller's context: canceling that context
// cancels outstanding lookups. The caller must still Close the client.
func New(ctx context.Context, c Config) (*Client, error) { return newWithHTTPClient(ctx, c, nil) }

// The client seam is package-private: callers cannot substitute a transport to forge a receipt.
func newWithHTTPClient(ctx context.Context, c Config, testClient *http.Client) (*Client, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "GitLab client requires a caller context")
	}
	if ctx.Err() != nil {
		return nil, e.New(e.CodeTimeout, "GitLab client context is already canceled")
	}
	u, err := url.Parse(c.APIURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Opaque != "" || u.RawPath != "" || u.Path != "/api/v4" || strings.ContainsAny(c.APIURL, "\r\n\x00?#") || u.String() != c.APIURL || c.ProjectID == 0 {
		return nil, e.New(e.CodeUsage, "GitLab API requires an exact HTTPS API base and project")
	}
	if c.JobToken == "" || c.ReadCredential == "" || (c.ReadCredentialKind != "job-token" && c.ReadCredentialKind != "private-token") || strings.ContainsAny(c.JobToken+c.ReadCredential, "\r\n") {
		return nil, e.New(e.CodeUsage, "GitLab API requires explicit scoped credentials")
	}
	// Do not inherit http.DefaultTransport: it is a mutable package global and
	// cannot serve as an authority boundary for provider responses.
	h := &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
	}}
	if testClient != nil {
		*h = *testClient
	}
	h.Jar = nil
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if h.Timeout <= 0 || h.Timeout > 30*time.Second {
		h.Timeout = 30 * time.Second
	}
	lifetime, cancel := context.WithCancel(ctx)
	return &Client{base: u, project: c.ProjectID, jobToken: c.JobToken, readCredential: c.ReadCredential, readKind: c.ReadCredentialKind, http: h, lifetime: lifetime, cancel: cancel}, nil
}

// Close revokes the client, cancels and joins active lookups, and closes its
// own idle connections. It is safe to call repeatedly and concurrently.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		if c.cancel != nil {
			c.cancel()
		}
	}
	c.mu.Unlock()
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
	c.jobToken, c.readCredential = "", ""
	return nil
}

type apiJob struct {
	ID        uint64     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	StartedAt *time.Time `json:"started_at"`
	Ref       string     `json:"ref"`
	Commit    struct {
		ID string `json:"id"`
	} `json:"commit"`
	Pipeline struct {
		ID        uint64 `json:"id"`
		ProjectID uint64 `json:"project_id"`
		SHA       string `json:"sha"`
		Ref       string `json:"ref"`
		Source    string `json:"source"`
	} `json:"pipeline"`
}

func (c *Client) get(ctx context.Context, endpoint, kind, credential string, out any) error {
	return c.getQuery(ctx, endpoint, "", kind, credential, out, nil)
}
func (c *Client) getQuery(ctx context.Context, endpoint, query, kind, credential string, out any, next *string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "GitLab lookup requires a context")
	}
	if ctx.Err() != nil {
		return e.New(e.CodeTimeout, "GitLab lookup canceled")
	}
	u := *c.base
	u.Path += endpoint
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return e.New(e.CodeUsage, "GitLab lookup request invalid")
	}
	if kind == "job-token" {
		req.Header.Set("JOB-TOKEN", credential)
	} else {
		req.Header.Set("PRIVATE-TOKEN", credential)
	}
	resp, err := c.lookupGET(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return e.New(e.CodeUnavailable, "GitLab lookup returned non-success status")
	}
	if next != nil {
		values, ok := resp.Header[http.CanonicalHeaderKey("X-Next-Page")]
		if !ok || len(values) != 1 {
			return e.New(e.CodeVerifyFailed, "GitLab pagination metadata missing or ambiguous")
		}
		*next = values[0]
	}
	if resp.ContentLength > maxPageBytes {
		return e.New(e.CodeVerifyFailed, "GitLab response exceeds byte limit")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	closeErr := resp.Body.Close()
	if ctx.Err() != nil {
		return e.New(e.CodeTimeout, "GitLab lookup canceled")
	}
	if err != nil || closeErr != nil || len(raw) > maxPageBytes || (resp.ContentLength >= 0 && int64(len(raw)) != resp.ContentLength) {
		return e.New(e.CodeVerifyFailed, "GitLab response could not be read within byte limit")
	}
	if canonical.ValidateJSONDocument(raw) != nil || json.Unmarshal(raw, out) != nil {
		return e.New(e.CodeVerifyFailed, "GitLab response is invalid JSON")
	}
	return nil
}
func validStatus(s string) bool {
	switch s {
	case "created", "pending", "preparing", "running", "success", "failed", "canceled", "canceling", "skipped", "manual", "scheduled", "waiting_for_resource", "waiting_for_callback":
		return true
	}
	return false
}
func validJob(j apiJob, id Identity) bool {
	return j.ID > 0 && j.Name != "" && validStatus(j.Status) && j.Pipeline.ID == id.PipelineID && j.Pipeline.ProjectID == id.ProjectID && j.Pipeline.SHA == id.Commit && j.Commit.ID == id.Commit && j.Ref == id.Ref && j.Pipeline.Ref == id.Ref && (j.Pipeline.Source == "" || j.Pipeline.Source == id.Source)
}

func safeDescription(s string) bool {
	return s != "" && len(s) <= 512 && !strings.ContainsFunc(s, unicode.IsControl)
}

// Current authenticates the current job with its own job token and enumerates
// only the provider's current (non-retried) jobs for that exact pipeline.
func (c *Client) Current(ctx context.Context, id Identity, expectedJobNames []string) (*Receipt, error) {
	if ctx == nil || c == nil {
		return nil, e.New(e.CodeUsage, "GitLab current identity or job set is invalid")
	}
	c.mu.Lock()
	if c.closed || c.lifetime == nil {
		c.mu.Unlock()
		return nil, e.New(e.CodeProvenanceMissing, "GitLab client is closed")
	}
	lifetime := c.lifetime
	// context.AfterFunc registration below is asynchronous, so an already-canceled
	// lifetime must be refused synchronously before any lookup work is admitted.
	if lifetime.Err() != nil {
		c.mu.Unlock()
		return nil, e.New(e.CodeTimeout, "GitLab client lifetime is canceled")
	}
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	stopLifetime := context.AfterFunc(lifetime, stop)
	defer stopLifetime()
	if c.base == nil || c.http == nil || c.project == 0 || (id.APIURL != "" && id.APIURL != c.base.String()) || id.ProjectID != c.project || id.PipelineID == 0 || id.JobID == 0 || !commitPattern.MatchString(id.Commit) || len(expectedJobNames) == 0 || len(expectedJobNames) > 5000 {
		return nil, e.New(e.CodeUsage, "GitLab current identity or job set is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	expected := make(map[string]bool, len(expectedJobNames))
	for _, n := range expectedJobNames {
		if n == "" || len(n) > 255 || !strings.HasPrefix(n, "nvkr:") || strings.ContainsAny(n, "\r\n\x00") || expected[n] {
			return nil, e.New(e.CodeUsage, "GitLab expected job set is invalid")
		}
		expected[n] = true
	}
	var project struct {
		ID              uint64          `json:"id"`
		CIConfigPath    json.RawMessage `json:"ci_config_path"`
		RegistryEnabled *bool           `json:"container_registry_enabled"`
		RegistryPrefix  *string         `json:"container_registry_image_prefix"`
	}
	projectPath := "/projects/" + strconv.FormatUint(id.ProjectID, 10)
	if err := c.get(ctx, projectPath, c.readKind, c.readCredential, &project); err != nil {
		return nil, err
	}
	if project.ID != id.ProjectID || len(project.CIConfigPath) == 0 {
		return nil, e.New(e.CodeVerifyFailed, "GitLab project config path is missing or foreign")
	}
	var configPath *string
	if json.Unmarshal(project.CIConfigPath, &configPath) != nil || (configPath != nil && *configPath != "" && *configPath != ".gitlab-ci.yml") {
		return nil, e.New(e.CodeVerifyFailed, "GitLab project config path is unsupported")
	}
	var pipeline struct {
		ID        uint64 `json:"id"`
		ProjectID uint64 `json:"project_id"`
		SHA       string `json:"sha"`
		Ref       string `json:"ref"`
		Source    string `json:"source"`
	}
	if err := c.get(ctx, projectPath+"/pipelines/"+strconv.FormatUint(id.PipelineID, 10), c.readKind, c.readCredential, &pipeline); err != nil {
		return nil, err
	}
	if pipeline.ID != id.PipelineID || pipeline.ProjectID != id.ProjectID || pipeline.SHA != id.Commit || !safeDescription(pipeline.Ref) || !safeDescription(pipeline.Source) {
		return nil, e.New(e.CodeVerifyFailed, "GitLab pipeline identity does not match")
	}
	if (id.Ref != "" && id.Ref != pipeline.Ref) || (id.Source != "" && id.Source != pipeline.Source) {
		return nil, e.New(e.CodeVerifyFailed, "GitLab pipeline selection does not match")
	}
	id.APIURL, id.Ref, id.Source = c.base.String(), pipeline.Ref, pipeline.Source
	var self apiJob
	if err := c.get(ctx, "/job", "job-token", c.jobToken, &self); err != nil {
		return nil, err
	}
	if !validJob(self, id) || self.ID != id.JobID || !expected[self.Name] || self.Status != "running" {
		return nil, e.New(e.CodeVerifyFailed, "GitLab current job identity does not match")
	}
	found, pages, err := c.currentJobs(ctx, id, self, expected)
	if err != nil {
		return nil, err
	}
	if pages > 1 {
		// GitLab pagination is not an atomic snapshot. Confirm a multi-page
		// inventory once within the same lookup deadline, refusing observed
		// membership or status changes rather than retrying toward success.
		confirmed, _, err := c.currentJobs(ctx, id, self, expected)
		if err != nil {
			return nil, err
		}
		if !maps.Equal(found, confirmed) {
			return nil, e.New(e.CodeVerifyFailed, "GitLab current job map changed during pagination")
		}
		found = confirmed
	}
	registry := ProjectRegistry{}
	if project.RegistryEnabled != nil && project.RegistryPrefix != nil {
		registry = ProjectRegistry{Present: true, Enabled: *project.RegistryEnabled, Prefix: *project.RegistryPrefix}
	}
	var startedAt time.Time
	if self.StartedAt != nil {
		startedAt = self.StartedAt.UTC()
	}
	return &Receipt{identity: id, jobs: found, registry: registry, startedAt: startedAt}, nil
}

func (c *Client) currentJobs(ctx context.Context, id Identity, self apiJob, expected map[string]bool) (map[string]Job, int, error) {
	found := make(map[string]Job, len(expected))
	ids := make(map[uint64]bool)
	for page := 1; page <= maxPages; page++ {
		endpoint := "/projects/" + strconv.FormatUint(id.ProjectID, 10) + "/pipelines/" + strconv.FormatUint(id.PipelineID, 10) + "/jobs?per_page=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(page) + "&include_retried=false"
		// Set RawQuery separately so URL path escaping cannot change endpoint semantics.
		path, query, _ := strings.Cut(endpoint, "?")
		var jobs []apiJob
		var next string
		if err := c.getPage(ctx, path, query, &jobs, &next); err != nil {
			return nil, 0, err
		}
		for _, j := range jobs {
			if !strings.HasPrefix(j.Name, "nvkr:") {
				continue
			}
			if !expected[j.Name] || !validJob(j, id) || found[j.Name].ID != 0 || ids[j.ID] {
				return nil, 0, e.New(e.CodeVerifyFailed, "GitLab current job map is ambiguous or foreign")
			}
			found[j.Name] = Job{j.ID, j.Name, j.Status}
			ids[j.ID] = true
		}
		if len(jobs) > pageSize {
			return nil, 0, e.New(e.CodeVerifyFailed, "GitLab page exceeds requested size")
		}
		if next == "" {
			if len(found) != len(expected) || found[self.Name].ID != self.ID || found[self.Name].Status != "running" {
				return nil, 0, e.New(e.CodeVerifyFailed, "GitLab current job map is incomplete")
			}
			return found, page, nil
		}
		if next != strconv.Itoa(page+1) || len(jobs) == 0 {
			return nil, 0, e.New(e.CodeVerifyFailed, "GitLab pagination is inconsistent")
		}
	}
	return nil, 0, e.New(e.CodeVerifyFailed, "GitLab current job map exceeds page limit")
}
func (c *Client) getPage(ctx context.Context, path, query string, out any, next *string) error {
	return c.getQuery(ctx, path, query, c.readKind, c.readCredential, out, next)
}
