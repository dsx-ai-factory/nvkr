// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	provider "github.com/dsx-ai-factory/nvkr/pkg/nvkr/gitlab"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/google/go-containerregistry/pkg/name"
)

// GitLabJobContext describes the caller. Only the provider owner authenticates it.
type GitLabJobContext struct {
	ProjectID, PipelineID, JobID uint64
	Commit                       string
}
type GitLabNodeOptions struct {
	Root           string
	Job            GitLabJobContext
	Stdout, Stderr io.Writer
}

// GitLabFinalizer is a process-local, revocable carrier admission. A copied handle
// shares revocation; neither its zero value nor serialized descriptions grant it.
type GitLabFinalizer struct{ state *gitlabAdmission }

// After admission, mu serializes access to mutable fields shared with Close.
// The operation and finalizer paths hold it across their state helper calls;
// Close cancels the lifetime before waiting for that lock.
type gitlabAdmission struct {
	mu         sync.Mutex
	closed     bool
	lifetime   context.Context
	cancel     context.CancelFunc
	client     *provider.Client
	identity   provider.Identity
	names      []string
	digest     string
	config     provider.Config
	receipt    *provider.Receipt
	registry   provider.ProjectRegistry
	acquired   map[string]provider.Job
	transport  *http.Transport
	httpClient *http.Client
}

func (owner *GitLabFinalizer) Close() error {
	if owner == nil || owner.state == nil {
		return nil
	}
	if owner.state.cancel != nil {
		owner.state.cancel()
	}
	owner.state.mu.Lock()
	defer owner.state.mu.Unlock()
	owner.state.closed = true
	if owner.state.client != nil {
		_ = owner.state.client.Close()
	}
	owner.state.client = nil
	owner.state.receipt = nil
	owner.state.config = provider.Config{}
	if owner.state.transport != nil {
		owner.state.transport.CloseIdleConnections()
	}
	owner.state.transport = nil
	owner.state.httpClient = nil
	return nil
}

func GitLabAttempt(job GitLabJobContext) (string, error) {
	if job.ProjectID == 0 || job.PipelineID == 0 || job.JobID == 0 || len(job.Commit) != 40 {
		return "", e.New(e.CodeUsage, "GitLab attempt requires canonical native identity")
	}
	decoded, err := hex.DecodeString(job.Commit)
	if err != nil || len(decoded) != 20 || strings.ToLower(job.Commit) != job.Commit {
		return "", e.New(e.CodeUsage, "GitLab source commit must be canonical hexadecimal")
	}
	return fmt.Sprintf("gitlab-%d-%d-%d", job.ProjectID, job.PipelineID, job.JobID), nil
}

// GitLabOutcomePath describes the native operation metadata member. It grants no
// provider or artifact authority; consumers must authenticate its current job.
func GitLabOutcomePath(nodeID string) string { return gitlabOutcomePath(nodeID) }

// GitLabCarrierPath describes one exact transport member, not a payload identity.
func GitLabCarrierPath(nodeID, artifactID string) string {
	return gitlabCarrierPath(nodeID, artifactID)
}

func gitlabOutcomePath(id string) string {
	return ".nvkr/gitlab/outcomes/" + shortStableName(id) + ".json"
}
func gitlabNodeCarrierDirectory(id string) string {
	return ".nvkr/gitlab/carriers/" + shortStableName(id)
}
func gitlabCarrierPath(node, artifact string) string {
	return gitlabNodeCarrierDirectory(node) + "/" + shortStableName(artifact) + ".tar"
}

type gitlabCarrier struct {
	Artifact string `json:"artifact"`
	Digest   string `json:"digest"`
	Bytes    int64  `json:"bytes"`
}
type gitlabInput struct {
	Artifact      string `json:"artifact"`
	ProducerJobID uint64 `json:"producerJobID"`
}

type gitlabOutcome struct {
	Schema     string          `json:"schema"`
	APIURL     string          `json:"apiURL"`
	ProjectID  uint64          `json:"projectID"`
	PipelineID uint64          `json:"pipelineID"`
	JobID      uint64          `json:"jobID"`
	Commit     string          `json:"commit"`
	Plan       string          `json:"plan"`
	Node       string          `json:"node"`
	Status     string          `json:"status"`
	ExitCode   int             `json:"exitCode"`
	Carriers   []gitlabCarrier `json:"carriers"`
	Inputs     []gitlabInput   `json:"inputs"`
}

func admitGitLabJob(ctx context.Context, plan *Plan, job GitLabJobContext, role string) (*GitLabFinalizer, error) {
	if err := ValidateGitLabPlan(ctx, plan); err != nil {
		return nil, err
	}
	if _, err := GitLabAttempt(job); err != nil {
		return nil, err
	}
	if err := CheckGenerated(ctx, plan, EngineGitLab, "."); err != nil {
		return nil, err
	}
	raw, err := readSmallFactoryFile(ctx, ".gitlab-ci.yml", "operator GitLab root")
	if err != nil {
		return nil, err
	}
	if err := gitlabRootProfile(raw); err != nil {
		return nil, err
	}
	policy := plan.Environment.GitLab
	if policy.ProjectID != job.ProjectID {
		return nil, e.New(e.CodePipelineDrift, "GitLab project differs from authored policy")
	}
	names, err := GitLabJobNames(ctx, plan)
	if err != nil {
		return nil, err
	}
	if err := gitlabCheckout(ctx, job.Commit, []string{".gitlab-ci.yml", GitLabPlanPath, GitLabManifestPath, GitLabWorkflowPath}); err != nil {
		return nil, err
	}
	config := provider.Config{APIURL: policy.APIURL, ProjectID: policy.ProjectID, JobToken: os.Getenv("CI_JOB_TOKEN"), ReadCredential: os.Getenv(strings.TrimPrefix(policy.APIReadCredentialRef, "gitlab:")), ReadCredentialKind: policy.APIReadCredentialKind}
	client, err := provider.New(ctx, config)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = client.Close()
		}
	}()
	identity := provider.Identity{APIURL: policy.APIURL, ProjectID: job.ProjectID, PipelineID: job.PipelineID, JobID: job.JobID, Commit: job.Commit}
	receipt, err := client.Current(ctx, identity, names)
	if err != nil {
		return nil, err
	}
	actual, ok := receipt.Job(role)
	if !ok || actual.ID != job.JobID || actual.Status != "running" {
		return nil, e.New(e.CodeProvenanceMissing, "GitLab caller is not the current running generated role")
	}
	registry := provider.ProjectRegistry{}
	if gitlabJobTokenPublication(plan) {
		registry = receipt.ProjectRegistry()
		if err := admitGitLabRegistry(plan, registry); err != nil {
			return nil, err
		}
	}
	digest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	accepted = true
	return &GitLabFinalizer{state: &gitlabAdmission{lifetime: lifetime, cancel: cancel, client: client, identity: receipt.CurrentIdentity(), names: names, digest: digest, config: config, receipt: receipt, registry: registry, acquired: map[string]provider.Job{role: actual}}}, nil
}
func AdmitGitLabFinalizer(ctx context.Context, plan *Plan, job GitLabJobContext) (*GitLabFinalizer, error) {
	return admitGitLabJob(ctx, plan, job, gitlabFinalizerJob)
}

// current requires the caller to hold state.mu.
func (state *gitlabAdmission) current(ctx context.Context) (*provider.Receipt, error) {
	if state.closed || state.client == nil {
		return nil, e.New(e.CodeProvenanceMissing, "GitLab admission is closed")
	}
	receipt, err := state.client.Current(ctx, state.identity, state.names)
	if err != nil {
		return nil, err
	}
	if state.registry.Present && receipt.ProjectRegistry() != state.registry {
		return nil, e.New(e.CodeProvenanceMissing, "GitLab project registry metadata changed during the operation")
	}
	state.receipt = receipt
	return receipt, nil
}

func gitlabJobTokenPublication(plan *Plan) bool {
	return plan != nil && plan.Environment.Publication != nil && plan.Environment.Publication.CredentialRef == "gitlab:CI_JOB_TOKEN"
}

// admitGitLabRegistry joins the planned namespace to metadata returned for the
// authenticated project. A lexical sibling is never treated as a child.
func admitGitLabRegistry(plan *Plan, registry provider.ProjectRegistry) error {
	if !registry.Present || !registry.Enabled || registry.Prefix == "" {
		return e.New(e.CodeProvenanceMissing, "GitLab project registry metadata is unavailable or disabled")
	}
	project, err := name.NewRepository(registry.Prefix, name.StrictValidation)
	if err != nil || project.Name() != registry.Prefix || project.Scheme() != "https" {
		return e.New(e.CodeProvenanceMissing, "GitLab project registry namespace is invalid")
	}
	target, err := name.NewRepository(plan.Environment.Publication.RepositoryPrefix, name.StrictValidation)
	if err != nil || target.Name() != plan.Environment.Publication.RepositoryPrefix || target.RegistryStr() != project.RegistryStr() || (target.RepositoryStr() != project.RepositoryStr() && !strings.HasPrefix(target.RepositoryStr(), project.RepositoryStr()+"/")) {
		return e.New(e.CodeProvenanceMissing, "GitLab publication is outside the authenticated project registry")
	}
	return nil
}

// RunGitLabNode authenticates the actual current role before compilation, source
// acquisition or artifact access, then delegates exactly one native operation.
func RunGitLabNode(ctx context.Context, plan *Plan, nodeID string, options GitLabNodeOptions) error {
	if err := ValidateGitLabPlan(ctx, plan); err != nil {
		return err
	}
	nodes, _ := planIndex(plan)
	node, ok := nodes[nodeID]
	if !ok {
		return e.New(e.CodeInvalidCatalog, "GitLab Plan has no selected node")
	}
	if options.Root != "" && options.Root != "." {
		return e.New(e.CodeEngineCapability, "GitLab native operation requires its checked-out working directory")
	}
	budget, err := gitlabOperationBudget(plan, node)
	if err != nil {
		return err
	}
	// Bound initial native admission too. The authenticated start below narrows
	// this bound to the actual job clock, including Runner checkout/startup.
	admissionCtx, stopAdmission := context.WithTimeout(ctx, budget+15*time.Minute)
	defer stopAdmission()
	owner, err := admitGitLabJob(admissionCtx, plan, options.Job, gitlabNodeJob(nodeID))
	if err != nil {
		return err
	}
	defer func() { _ = owner.Close() }()
	preparationDeadline, jobDeadline, err := gitlabPhaseDeadlines(ctx, owner.state.receipt.StartedAt(), time.Now(), budget)
	if err != nil {
		return err
	}
	jobCtx, stopJob := context.WithDeadline(owner.state.lifetime, jobDeadline)
	defer stopJob()
	ctx, stopPreparation := context.WithDeadline(jobCtx, preparationDeadline)
	defer stopPreparation()
	resolved, err := plan.ExecutionFor(node)
	if err != nil {
		return err
	}
	platform := gitlabResolvedPlatform(plan, resolved)
	bootstrap, ok := owner.state.receipt.Job(gitlabBootstrapJob(platform))
	if !ok || bootstrap.Status != "success" {
		return e.New(e.CodeProvenanceMissing, "GitLab runner bootstrap is not current and successful")
	}
	owner.state.acquired[gitlabBootstrapJob(platform)] = bootstrap

	if runtime.GOOS+"/"+runtime.GOARCH != platform {
		return e.New(e.CodeEngineCapability, "GitLab native host does not match the admitted operation platform")
	}
	if _, err := exec.LookPath(plan.Environment.RuntimeBinary()); err != nil {
		return e.New(e.CodeEngineCapability, "GitLab native operation runtime is not available on its selected runner")
	}
	expected, snapshot, err := CaptureCompileFiles(ctx, ".", plan.DefinitionPath, plan.EnvironmentPath, plan.GeneratorVersion)
	if err != nil {
		return err
	}
	digest, err := expected.Digest()
	if err != nil {
		return err
	}
	if digest != owner.state.digest {
		return e.New(e.CodePipelineDrift, "GitLab operation differs from exact authored Plan")
	}
	paths := make([]string, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		paths = append(paths, file.Path)
	}
	if err := gitlabCheckout(ctx, options.Job.Commit, paths); err != nil {
		return err
	}
	owner.state.mu.Lock()
	defer owner.state.mu.Unlock()
	_, artifacts := planIndex(plan)
	if err := owner.state.restore(ctx, plan, node.Inputs, artifacts); err != nil {
		return err
	}
	if time.Until(jobDeadline) < budget+gitlabCompletionAllowance {
		return e.New(e.CodeTimeout, "GitLab preparation exhausted the reserved execution budget")
	}
	stopPreparation()
	bounded, cancel := context.WithTimeout(jobCtx, budget)
	defer cancel()
	var runErr error
	switch node.Kind {
	case NodeFetch:
		runErr = executeSourceFetchInContext(bounded, plan, node.ID, []string{"PATH=" + os.Getenv("PATH")}, options.Stdout, options.Stderr, (pipeline.Local{}).ExecNode)
	case NodePublish:
		runErr = executeGitLabPublication(bounded, plan, node, artifacts, owner.state)
	default:
		placement := compiledPlacement(plan)
		placement.contained = func(_ *Plan, n Node) bool { return n.Container != "" }
		placement.launch = func(ctx context.Context, n Node) error {
			if err := checkNodeContainerPlatform(ctx, plan, n, pipeline.CheckContainerPlatform); err != nil {
				return err
			}
			root, err := filepath.Abs(".")
			if err != nil {
				return err
			}
			helper := "/workspace/.nvkr/gitlab/bootstrap/" + strings.ReplaceAll(platform, "/", "-") + "/nvkr-exec"
			access, err := plan.runtimeImageAccess(n, "local")
			if err != nil {
				return err
			}
			location := utilityRunnerLocation{runtime: plan.Environment.RuntimeBinary(), imageAccess: access, mountSource: root, mountTarget: "/workspace", helper: helper, plan: GitLabPlanPath, planDigest: owner.state.digest}
			if location.runtime == "docker" {
				location.user = localContainerUser()
				location.home = true
			}
			return executeUtilityNode(ctx, n, location, options.Stdout, options.Stderr)
		}
		if node.IRNode != "" && resolved.Container != "" {
			if err := checkNodeContainerPlatform(bounded, plan, node, pipeline.CheckContainerPlatform); err != nil {
				return err
			}
		}
		runErr = newOperationSession(plan, placement, options.Stdout, options.Stderr).execute(bounded, node)
	}
	// Completion shares the native job deadline; it cannot consume the
	// producer time reserved above or run beyond the generated allowance.
	ctx = jobCtx
	outcome := gitlabOutcome{Schema: "nvkr.dev/gitlab-operation/v1", APIURL: owner.state.identity.APIURL, ProjectID: options.Job.ProjectID, PipelineID: options.Job.PipelineID, JobID: options.Job.JobID, Commit: options.Job.Commit, Plan: owner.state.digest, Node: nodeID, Status: "failed", ExitCode: e.ExitCode(runErr), Carriers: []gitlabCarrier{}}
	outcome.Inputs = make([]gitlabInput, 0, len(node.Inputs))
	for _, id := range node.Inputs {
		source := owner.state.acquired[gitlabNodeJob(artifacts[id].Producer)]
		outcome.Inputs = append(outcome.Inputs, gitlabInput{Artifact: id, ProducerJobID: source.ID})
	}
	if runErr == nil {
		outcome.Status = "success"
		for _, id := range node.Outputs {
			artifact := artifacts[id]
			path := gitlabCarrierPath(nodeID, id)
			if err := transportarchive.Pack(ctx, path, ".", []string{artifact.Path}); err != nil {
				runErr = err
				break
			}
			held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "GitLab producer carrier", MaxBytes: transportarchive.MaxArchiveBytes})
			if err != nil {
				runErr = err
				break
			}
			outcome.Carriers = append(outcome.Carriers, gitlabCarrier{Artifact: id, Digest: held.Digest(), Bytes: held.Size()})
			_ = held.Close()
		}
	}
	if runErr == nil {
		runErr = owner.state.revalidateAcquired(ctx)
	}
	if runErr != nil {
		outcome.Status = "failed"
		outcome.ExitCode = e.ExitCode(runErr)
	}
	raw, err := canonical.Marshal(outcome)
	if err != nil {
		return err
	}
	writeErr := commitGitHubDiskFile(ctx, gitlabOutcomePath(nodeID), func(file *os.File) error { _, err := file.Write(raw); return err })
	if runErr != nil {
		return runErr
	}
	return writeErr
}

const gitlabCompletionAllowance = 5 * time.Minute

// gitlabPhaseDeadlines reserves the complete compiled producer budget and a
// completion allowance inside the native job clock. Caller deadlines can only
// shorten that clock. The provider's own started_at is required, not CI text.
func gitlabPhaseDeadlines(ctx context.Context, started, now time.Time, budget time.Duration) (time.Time, time.Time, error) {
	if started.IsZero() || started.After(now) {
		return time.Time{}, time.Time{}, e.New(e.CodeProvenanceMissing, "GitLab current job start time is missing or in the future")
	}
	deadline := started.Add(budget + 15*time.Minute)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	preparation := deadline.Add(-budget - gitlabCompletionAllowance)
	if !now.Before(preparation) {
		return time.Time{}, time.Time{}, e.New(e.CodeTimeout, "GitLab job has insufficient time for its reserved execution budget")
	}
	return preparation, deadline, nil
}

// executeGitLabPublication uses the native publication and closure owners. A
// successful push alone is not an admitted operation: the complete release is
// pulled and inspected independently before its reference becomes an output.
func executeGitLabPublication(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, admission *gitlabAdmission) (returnErr error) {
	if node.ID != "publish/release" || node.Target != "release" || len(node.Inputs) != 1 || len(node.Outputs) != 1 {
		return e.New(e.CodePipelineDrift, "GitLab publication has unexpected graph bindings")
	}
	target, err := runtimeTarget(plan, node.Target)
	if err != nil {
		return err
	}
	repo, err := name.NewRepository(target.Ref, name.StrictValidation)
	authenticated := !target.Anonymous && target.CredentialRef == "gitlab:CI_JOB_TOKEN"
	if err != nil || repo.Name() != target.Ref || repo.Scheme() != "https" || target.Kind != TargetOCI || (!authenticated && (!target.Anonymous || target.CredentialRef != "")) || target.PlainHTTP || len(target.ImageTags) != 0 {
		return e.New(e.CodeEngineCapability, "GitLab publication target is not an admitted HTTPS release")
	}
	options := delivery.RegistryOptions{Anonymous: true, IsolatedTransport: true}
	if authenticated {
		if admission == nil || admission.closed || admission.receipt == nil || admission.config.JobToken == "" || admission.registry != admission.receipt.ProjectRegistry() {
			return e.New(e.CodeProvenanceMissing, "GitLab publication requires its live current-job admission")
		}
		if err := admitGitLabRegistry(plan, admission.registry); err != nil {
			return err
		}
		issuer, err := url.Parse(admission.identity.APIURL)
		if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.Path != "/api/v4" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
			return e.New(e.CodeProvenanceMissing, "GitLab publication issuer is invalid")
		}
		options = delivery.RegistryOptions{Username: "gitlab-ci-token", Password: admission.config.JobToken, IsolatedTransport: true, Authority: &delivery.RegistryAuthority{RegistryHost: repo.RegistryStr(), TokenEndpoint: (&url.URL{Scheme: "https", Host: issuer.Host, Path: "/jwt/auth"}).String()}}
	}
	input, ok := artifacts[node.Inputs[0]]
	if !ok {
		return e.New(e.CodePipelineDrift, "GitLab publication input is missing")
	}
	output, ok := artifacts[node.Outputs[0]]
	if !ok {
		return e.New(e.CodePipelineDrift, "GitLab publication output is missing")
	}
	inspection, err := delivery.Inspect(ctx, input.Path)
	if err != nil {
		return err
	}
	if authenticated {
		rootRef := repo.Digest(inspection.RootDigest).Name()
		outputs, err := delivery.OutputReferences(rootRef, inspection)
		if err != nil {
			return err
		}
		evidence, err := delivery.EvidenceReferences(rootRef, inspection)
		if err != nil {
			return err
		}
		project, err := name.NewRepository(admission.registry.Prefix, name.StrictValidation)
		if err != nil {
			return e.New(e.CodeProvenanceMissing, "GitLab project registry namespace is invalid")
		}
		repositories := []string{repo.Name()}
		for _, references := range []map[string]string{outputs, evidence} {
			for _, reference := range references {
				item, err := name.NewDigest(reference, name.StrictValidation)
				if err != nil || item.Context().RegistryStr() != project.RegistryStr() || (item.Context().RepositoryStr() != project.RepositoryStr() && !strings.HasPrefix(item.Context().RepositoryStr(), project.RepositoryStr()+"/")) {
					return e.New(e.CodeProvenanceMissing, "GitLab publication member is outside the authenticated project registry")
				}
				repositories = append(repositories, item.Context().Name())
			}
		}
		options.Authority.Repositories = uniqueSorted(repositories)
	}
	// Inspection may outlive an upstream attempt. Refuse an already superseded
	// closure before the first remote write; completion still checks for changes
	// during publication under the same original acquired observations.
	if admission != nil {
		if err := admission.revalidateAcquired(ctx); err != nil {
			return err
		}
	}
	ref, err := delivery.PushWithOptions(ctx, input.Path, target.Ref, delivery.PushOptions{Registry: options, ExpectedRootDigest: inspection.RootDigest})
	if err != nil {
		return delivery.SafeRegistryPublishError(ctx, err)
	}
	digest, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil || digest.Context().Name() != target.Ref || digest.DigestStr() != inspection.RootDigest {
		return e.New(e.CodeVerifyFailed, "GitLab published reference differs from the admitted release")
	}
	dir, err := os.MkdirTemp("", "nvkr-gitlab-publication-readback-")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = joinFactoryCleanupError(returnErr, os.RemoveAll(dir), "remove private GitLab publication readback")
	}()
	readback, err := delivery.Pull(ctx, ref, filepath.Join(dir, "release"), options)
	if err != nil {
		return delivery.SafeRegistryPublishedReadbackError(ctx, err)
	}
	if readback.RootDigest != inspection.RootDigest {
		return delivery.SafeRegistryPublishedReadbackError(ctx, e.New(e.CodeVerifyFailed, "GitLab publication readback differs from the admitted release"))
	}
	return writeFactoryFile(ctx, output.Path, []byte(ref+"\n"), 0o644)
}

func (state *gitlabAdmission) restore(ctx context.Context, plan *Plan, ids []string, artifacts map[string]Artifact) error {
	byProducer := map[string][]string{}
	for _, id := range ids {
		artifact, ok := artifacts[id]
		if !ok {
			return e.New(e.CodePipelineDrift, "GitLab restore has an unknown artifact")
		}
		byProducer[artifact.Producer] = append(byProducer[artifact.Producer], id)
	}
	if len(byProducer) == 0 {
		return state.revalidateAcquired(ctx)
	}
	// Authenticate one bounded snapshot for the complete acquisition, then
	// compare every retained original observation after all transfers. A refresh
	// per producer adds no atomicity and makes provider load grow with artifacts.
	receipt, err := state.current(ctx)
	if err != nil {
		return err
	}
	jobs := gitlabJobDescriptions(receipt, state.names)
	nodes, _ := planIndex(plan)
	validated := map[string]map[string]gitlabCarrier{}
	visiting := map[string]bool{}
	for _, producer := range sortedKeys(byProducer) {
		carriers, err := state.validateProducerClosure(ctx, nodes, artifacts, producer, jobs, validated, visiting)
		if err != nil {
			return err
		}
		job := jobs[gitlabNodeJob(producer)]
		for _, id := range uniqueSorted(byProducer[producer]) {
			item, ok := carriers[id]
			if !ok {
				return e.New(e.CodeProvenanceMissing, "GitLab selected carrier is missing")
			}
			path := gitlabCarrierPath(producer, id)
			if err := state.download(ctx, job.ID, path, item); err != nil {
				return err
			}
			if err := transportarchive.Unpack(ctx, path, ".", []string{artifacts[id].Path}); err != nil {
				return err
			}
			if err := removeFactoryCarrier(path); err != nil {
				return err
			}
		}
	}
	return state.revalidateAcquired(ctx)
}

// validateProducerClosure authenticates every ancestor outcome against the same
// provider snapshot, once per acquisition. Ancestor carriers are descriptions
// only: restore downloads just the consumer's selected artifact payloads.
func (state *gitlabAdmission) validateProducerClosure(ctx context.Context, nodes map[string]Node, artifacts map[string]Artifact, producer string, jobs map[string]provider.Job, validated map[string]map[string]gitlabCarrier, visiting map[string]bool) (map[string]gitlabCarrier, error) {
	if carriers, ok := validated[producer]; ok {
		return carriers, nil
	}
	node, ok := nodes[producer]
	if !ok || visiting[producer] {
		return nil, e.New(e.CodePipelineDrift, "GitLab producer closure is unknown or cyclic")
	}
	job, ok := jobs[gitlabNodeJob(producer)]
	if !ok || job.Status != "success" {
		return nil, e.New(e.CodeReleaseIncomplete, "GitLab producer is not current and successful")
	}
	if err := state.retain(gitlabNodeJob(producer), job); err != nil {
		return nil, err
	}
	raw, err := state.member(ctx, job.ID, gitlabOutcomePath(producer), 4<<20)
	if err != nil {
		return nil, err
	}
	carriers, err := validateGitLabProducerOutcome(raw, state.identity, state.digest, job.ID, producer, nodes, artifacts, jobs)
	if err != nil {
		return nil, err
	}
	visiting[producer] = true
	defer delete(visiting, producer)
	for _, input := range node.Inputs {
		artifact, ok := artifacts[input]
		if !ok {
			return nil, e.New(e.CodePipelineDrift, "GitLab producer closure has an unknown input")
		}
		if _, err := state.validateProducerClosure(ctx, nodes, artifacts, artifact.Producer, jobs, validated, visiting); err != nil {
			return nil, err
		}
	}
	validated[producer] = carriers
	return carriers, nil
}

// retain preserves the first authenticated observation. Refreshing a provider
// description can invalidate an attempt, never silently rebind acquired bytes.
func (state *gitlabAdmission) retain(name string, job provider.Job) error {
	if state.acquired == nil {
		state.acquired = map[string]provider.Job{}
	}
	if previous, ok := state.acquired[name]; ok && previous != job {
		return e.New(e.CodeProvenanceMissing, "GitLab producer changed after admission")
	}
	state.acquired[name] = job
	return nil
}

// Revalidate confirms the original acquired job IDs without restoring artifacts
// or substituting refreshed producer attempts. Close revokes this operation.
func (owner *GitLabFinalizer) Revalidate(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "GitLab revalidation requires a context")
	}
	if owner == nil || owner.state == nil || owner.state.lifetime == nil {
		return e.New(e.CodeProvenanceMissing, "GitLab revalidation requires its live native admission")
	}
	state := owner.state
	if ctx.Err() != nil || state.lifetime.Err() != nil {
		return e.New(e.CodeTimeout, "GitLab revalidation lifetime is canceled")
	}
	bounded, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(state.lifetime, cancel)
	defer func() { stop(); cancel() }()
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.revalidateAcquired(bounded)
}

func (state *gitlabAdmission) revalidateAcquired(ctx context.Context) error {
	receipt, err := state.current(ctx)
	if err != nil {
		return err
	}
	for name, expected := range state.acquired {
		actual, ok := receipt.Job(name)
		if !ok || actual != expected {
			return e.New(e.CodeProvenanceMissing, "GitLab acquired job closure changed before consumption")
		}
	}
	return nil
}

func ValidateGitLabOutcomes(ctx context.Context, workspace string, plan *Plan, owner *GitLabFinalizer) error {
	if err := ValidateGitLabPlan(ctx, plan); err != nil {
		return err
	}
	if owner == nil || owner.state == nil {
		return e.New(e.CodeProvenanceMissing, "GitLab finalizer requires its live native admission")
	}
	state := owner.state
	bounded, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(state.lifetime, cancel)
	defer func() { stop(); cancel() }()
	ctx = bounded
	state.mu.Lock()
	defer state.mu.Unlock()
	digest, err := plan.Digest()
	if err != nil {
		return err
	}
	if digest != state.digest {
		return e.New(e.CodePipelineDrift, "GitLab finalizer Plan changed after admission")
	}
	if filepath.Clean(workspace) != filepath.Join(".nvkr", "factory") {
		return e.New(e.CodeEngineCapability, "GitLab finalizer requires the compiled workspace")
	}
	receipt, err := state.current(ctx)
	if err != nil {
		return err
	}
	self, ok := receipt.Job(gitlabFinalizerJob)
	if !ok || self.ID != state.identity.JobID || self.Status != "running" {
		return e.New(e.CodeProvenanceMissing, "GitLab finalizer is no longer current")
	}
	state.acquired = make(map[string]provider.Job, len(state.names))
	for _, name := range state.names {
		if name == gitlabFinalizerJob {
			state.acquired[name] = self
			continue
		}
		job, ok := receipt.Job(name)
		if !ok || job.Status != "success" {
			return e.New(e.CodeReleaseIncomplete, "GitLab complete job closure did not succeed")
		}
		state.acquired[name] = job
	}
	_, artifacts := planIndex(plan)
	ids := sortedKeys(artifacts)
	if err := state.restore(ctx, plan, ids, artifacts); err != nil {
		return err
	}
	if err := validateHostedOutcomes(ctx, workspace, plan); err != nil {
		return err
	}
	return state.revalidateAcquired(ctx)
}

func (state *gitlabAdmission) request(ctx context.Context, job uint64, path string) (*http.Response, error) {
	endpoint := strings.TrimRight(state.config.APIURL, "/") + "/projects/" + strconv.FormatUint(state.config.ProjectID, 10) + "/jobs/" + strconv.FormatUint(job, 10) + "/artifacts/" + url.PathEscape(path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, e.New(e.CodeFetchFailed, "GitLab artifact request is invalid")
	}
	if state.config.ReadCredentialKind == "job-token" {
		request.Header.Set("JOB-TOKEN", state.config.ReadCredential)
	} else {
		request.Header.Set("PRIVATE-TOKEN", state.config.ReadCredential)
	}
	if state.httpClient == nil {
		state.transport = &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
		state.httpClient = &http.Client{Transport: state.transport, Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	client := state.httpClient
	response, err := client.Do(request)
	if err != nil {
		return nil, classifyContext(ctx, e.New(e.CodeFetchFailed, "GitLab artifact transport failed"))
	}
	// Reuse the CI locator owner's two-request separation: the native API
	// authenticates the storage location, then a NEW request carries no API
	// credential/cookie. Further redirects and unsafe locations fail closed.
	// The selected provider controls that HTTPS storage destination; runner
	// egress policy supplies any additional destination restrictions.
	if response.StatusCode == http.StatusFound {
		locations := response.Header.Values("Location")
		_ = response.Body.Close()
		if len(locations) != 1 || len(locations[0]) > 8192 {
			return nil, e.New(e.CodeFetchFailed, "GitLab artifact requires one bounded native storage location")
		}
		location, err := url.Parse(locations[0])
		if err != nil || location.Scheme != "https" || location.Host == "" || location.User != nil || location.Fragment != "" {
			return nil, e.New(e.CodeFetchFailed, "GitLab artifact storage location is not admitted")
		}
		anonymous, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
		if err != nil {
			return nil, e.New(e.CodeFetchFailed, "GitLab artifact storage request is invalid")
		}
		response, err = client.Do(anonymous)
		if err != nil {
			return nil, classifyContext(ctx, e.New(e.CodeFetchFailed, "GitLab artifact storage transport failed"))
		}
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, e.New(e.CodeFetchFailed, "GitLab artifact response status %d", response.StatusCode)
	}
	return response, nil
}
func (state *gitlabAdmission) member(ctx context.Context, job uint64, path string, limit int64) ([]byte, error) {
	response, err := state.request(ctx, job, path)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, classifyContext(ctx, e.New(e.CodeFetchFailed, "GitLab metadata member exceeded its bounded read"))
	}
	return raw, nil
}
func (state *gitlabAdmission) download(ctx context.Context, job uint64, path string, item gitlabCarrier) error {
	response, err := state.request(ctx, job, path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return commitGitHubDiskFile(ctx, path, func(file *os.File) error {
		hash := sha256.New()
		size, err := copyContext(ctx, io.MultiWriter(file, hash), io.LimitReader(response.Body, item.Bytes+1))
		if err != nil {
			return e.New(e.CodeFetchFailed, "GitLab carrier transport failed")
		}
		if size != item.Bytes || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != item.Digest {
			return e.New(e.CodeVerifyFailed, "GitLab carrier bytes differ from current producer outcome")
		}
		return nil
	})
}

// Git owns revision/index/path syntax. The existing isolated source command
// owner excludes callbacks and credential helpers; no new Git parser is used.
func gitlabCheckout(ctx context.Context, commit string, paths []string) error {
	root, err := filepath.Abs(".")
	if err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "resolving GitLab checkout")
	}
	return sourcegit.VerifyCheckout(ctx, []string{"PATH=" + os.Getenv("PATH")}, root, commit, paths, 100000)
}

func validateGitLabProducerOutcome(raw []byte, identity provider.Identity, digest string, jobID uint64, producer string, nodes map[string]Node, artifacts map[string]Artifact, jobs map[string]provider.Job) (map[string]gitlabCarrier, error) {
	var outcome gitlabOutcome
	if err := canonical.UnmarshalExact(raw, &outcome); err != nil {
		return nil, e.New(e.CodeProvenanceMissing, "GitLab operation outcome is malformed")
	}
	if outcome.Schema != "nvkr.dev/gitlab-operation/v1" || outcome.APIURL != identity.APIURL || outcome.ProjectID != identity.ProjectID || outcome.PipelineID != identity.PipelineID || outcome.JobID != jobID || outcome.Commit != identity.Commit || outcome.Plan != digest || outcome.Node != producer || outcome.Status != "success" || outcome.ExitCode != 0 {
		return nil, e.New(e.CodeProvenanceMissing, "GitLab outcome does not bind its exact current producer")
	}
	node, ok := nodes[producer]
	if !ok || len(outcome.Carriers) != len(node.Outputs) {
		return nil, e.New(e.CodeProvenanceMissing, "GitLab outcome has incomplete carrier membership")
	}
	if len(outcome.Inputs) != len(node.Inputs) {
		return nil, e.New(e.CodeProvenanceMissing, "GitLab outcome omits exact original input producer bindings")
	}
	for index, input := range outcome.Inputs {
		if input.Artifact != node.Inputs[index] {
			return nil, e.New(e.CodeProvenanceMissing, "GitLab outcome input membership changed")
		}
		original, ok := jobs[gitlabNodeJob(artifacts[input.Artifact].Producer)]
		if !ok || original.Status != "success" || input.ProducerJobID == 0 || input.ProducerJobID != original.ID {
			return nil, e.New(e.CodeProvenanceMissing, "GitLab outcome input came from a superseded or unsuccessful producer")
		}
	}
	carriers := map[string]gitlabCarrier{}
	for index, item := range outcome.Carriers {
		if item.Artifact != node.Outputs[index] || !canonical.IsDigest(item.Digest) || item.Bytes <= 0 || item.Bytes > transportarchive.MaxArchiveBytes {
			return nil, e.New(e.CodeProvenanceMissing, "GitLab carrier does not bind exact planned output")
		}
		carriers[item.Artifact] = item
	}
	return carriers, nil
}

// This copies authenticated observations for private byte admission; it does not
// turn descriptions into a new provider receipt or durable retry authority.
func gitlabJobDescriptions(receipt *provider.Receipt, names []string) map[string]provider.Job {
	jobs := make(map[string]provider.Job, len(names))
	for _, name := range names {
		if job, ok := receipt.Job(name); ok {
			jobs[name] = job
		}
	}
	return jobs
}
