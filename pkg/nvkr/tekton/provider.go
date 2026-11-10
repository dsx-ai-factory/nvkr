// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package tekton owns authentication of Tekton execution and artifact handoffs.
// Kubernetes descriptions become receipts only after the selected API server
// authenticates their exact Run, task, specification and completion bindings.
package tekton

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	PlanAnnotation = "nvkr.dev/plan-digest"
	maxAPIBytes    = 8 << 20
	requestTimeout = 30 * time.Second
)

var resourceName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var resourceUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

var errChildPending = errors.New("controller child reference is not yet recorded")

// ProviderPolicy is independently selected trust. Neither a carrier nor a
// TaskRun supplies the API endpoint, CA, Run UID or expected complete Run specification.
// Token is an explicitly scoped read credential, never persisted in a receipt.
type ProviderPolicy struct {
	Server, Namespace, RunName, RunUID, PlanDigest string
	CertificateAuthority                           []byte
	RunSpec                                        json.RawMessage
	Token                                          string
}

// Provider holds a private snapshot of the admitted policy and HTTP transport.
// Call Close after all operations finish. It does not use ambient proxies,
// credentials, kubeconfig, system roots or cross-origin redirects.
type Provider struct {
	policy    ProviderPolicy
	client    *http.Client
	transport *http.Transport
}

func NewProvider(policy ProviderPolicy) (*Provider, error) {
	endpoint, err := url.Parse(policy.Server)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.Path != "" || endpoint.RawPath != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" || endpoint.String() != policy.Server ||
		!resourceName.MatchString(policy.Namespace) || !resourceName.MatchString(policy.RunName) ||
		!resourceUID.MatchString(policy.RunUID) || !canonical.IsDigest(policy.PlanDigest) ||
		policy.Token == "" || strings.ContainsAny(policy.Token, " \r\n\t\x00") {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton requires an HTTPS API, exact namespace/Run/UID/Plan and explicit read credential")
	}
	if len(policy.CertificateAuthority) == 0 || len(policy.CertificateAuthority) > 64<<10 {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton requires a bounded explicit API certificate authority")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(policy.CertificateAuthority) {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton API certificate authority is invalid")
	}
	spec, err := exactJSON(policy.RunSpec)
	if err != nil {
		return nil, err
	}
	policy.RunSpec = spec
	policy.CertificateAuthority = bytes.Clone(policy.CertificateAuthority)
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		MaxConnsPerHost:       4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &Provider{policy: policy, transport: transport, client: &http.Client{
		Transport: transport, Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return e.New(e.CodeUntrustedSigner, "Tekton API redirects are not admitted")
		},
	}}, nil
}

func (provider *Provider) Close() {
	if provider != nil && provider.transport != nil {
		provider.transport.CloseIdleConnections()
	}
}

// TaskExpectation selects one exact completed TaskRun, not a latest/name lookup.
// Spec and service account must also occur in the independently selected Pipeline.
type TaskExpectation struct {
	Name, RunName, RunUID, ServiceAccount string
	Spec                                  json.RawMessage
}

// TaskReceipt cannot be reconstructed from a serialized provider description.
// It authenticates execution and result transport, never artifact correctness,
// qualification, reproducibility or release authorization.
type TaskReceipt struct {
	invocation string
	builder    string
	taskUID    string
	planDigest string
	account    string
	state      string
	results    map[string]string
}

// ExecutionContext is descriptive provenance backed by an authenticated task
// receipt. It never invents a GitHub workflow or an OIDC certificate identity.
type ExecutionContext struct {
	builder, invocation string
	started, finished   time.Time
}

func (execution ExecutionContext) BuilderID() string     { return execution.builder }
func (execution ExecutionContext) InvocationID() string  { return execution.invocation }
func (execution ExecutionContext) StartedOn() time.Time  { return execution.started }
func (execution ExecutionContext) FinishedOn() time.Time { return execution.finished }
func (receipt *TaskReceipt) Context(started, finished time.Time) (ExecutionContext, error) {
	if receipt == nil || receipt.taskUID == "" || receipt.state == "failure" || started.IsZero() || finished.Before(started) {
		return ExecutionContext{}, e.New(e.CodeProvenanceMissing, "Tekton provenance requires an admitted task and bounded time window")
	}
	return ExecutionContext{receipt.builder, receipt.invocation, started.UTC(), finished.UTC()}, nil
}

func (receipt *TaskReceipt) Invocation() string {
	if receipt == nil {
		return ""
	}
	return receipt.invocation
}

func (receipt *TaskReceipt) TaskUID() string {
	if receipt == nil {
		return ""
	}
	return receipt.taskUID
}

// RequirePlan checks the private receipt binding before executable work. A
// running receipt admits only this task; completed receipts admit byte handoffs.
func (receipt *TaskReceipt) RequirePlan(digest, account string) error {
	if receipt == nil || receipt.taskUID == "" || receipt.planDigest != digest || receipt.account != account {
		return e.New(e.CodeVerifyFailed, "Tekton receipt does not bind the selected Plan and service account")
	}
	return nil
}

// State returns only an authenticated provider outcome: running, success or
// failure. It does not infer artifact validity from task completion.
func (receipt *TaskReceipt) State() string {
	if receipt == nil {
		return ""
	}
	return receipt.state
}

func (receipt *TaskReceipt) Result(name string) (string, error) {
	if receipt == nil || receipt.state != "success" {
		return "", e.New(e.CodeVerifyFailed, "Tekton result requires a successful task")
	}
	value, ok := receipt.results[name]
	if !ok {
		return "", e.New(e.CodeVerifyFailed, "Tekton task omitted the required result")
	}
	return value, nil
}

// RequireBytes binds a restored carrier to the digest recorded by its exact
// successful TaskRun. Callers must additionally validate the carrier's contents
// and exact Plan-derived producer/consumer membership before using any member.
func (receipt *TaskReceipt) RequireBytes(ctx context.Context, result string, raw []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if receipt == nil || receipt.taskUID == "" || receipt.state != "success" || !canonical.IsDigest(receipt.results[result]) {
		return e.New(e.CodeVerifyFailed, "Tekton carrier has no authenticated task result")
	}
	digest := sha256.Sum256(raw) // Exact blob integrity, not structured identity.
	if receipt.results[result] != "sha256:"+hex.EncodeToString(digest[:]) {
		return e.New(e.CodeVerifyFailed, "Tekton carrier bytes differ from the authenticated task result")
	}
	return nil
}

// AuthenticateTask checks both provider objects over the admitted TLS channel.
// A completed task from another Run, a substituted embedded TaskSpec, a retry,
// or a canceled PipelineRun cannot issue a receipt.
func (provider *Provider) AuthenticateTask(ctx context.Context, expected TaskExpectation) (*TaskReceipt, error) {
	receipt, err := provider.AuthenticateExecution(ctx, expected)
	if err != nil {
		return nil, err
	}
	if receipt.state != "success" {
		return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun did not complete successfully")
	}
	return receipt, nil
}

// AuthenticateStage discovers the controller-selected child, then rechecks its
// complete exact identity against the PipelineRun. Discovery itself is never a
// receipt and never selects a latest task from a list.
func (provider *Provider) AuthenticateStage(ctx context.Context, stage, account string, spec json.RawMessage) (*TaskReceipt, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if provider == nil || provider.client == nil || !resourceName.MatchString(stage) || !resourceName.MatchString(account) {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton stage requires an admitted provider and exact names")
	}
	var run pipelineRun
	if err := provider.get(ctx, "pipelineruns", provider.policy.RunName, &run); err != nil {
		return nil, err
	}
	var name string
	for _, child := range run.Status.ChildReferences {
		if child.PipelineTaskName == stage {
			if name != "" || !resourceName.MatchString(child.Name) {
				return nil, e.New(e.CodeVerifyFailed, "Tekton stage has an ambiguous child")
			}
			name = child.Name
		}
	}
	if name == "" {
		return nil, e.New(e.CodeVerifyFailed, "Tekton stage has no controller-recorded child")
	}
	var task taskRun
	if err := provider.get(ctx, "taskruns", name, &task); err != nil {
		return nil, err
	}
	return provider.AuthenticateExecution(ctx, TaskExpectation{Name: stage, RunName: name, RunUID: task.Metadata.UID, ServiceAccount: account, Spec: spec})
}

// AwaitExecution admits initial task startup after the controller records its
// child reference. It waits at most 30 seconds, only for that missing status
// field; every attempt still authenticates the exact Run and specification.
// No task work or receipt is permitted before the complete binding passes.
// Active leases use AuthenticateExecution instead and never delay revocation.
func (provider *Provider) AwaitExecution(ctx context.Context, expected TaskExpectation) (*TaskReceipt, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		receipt, err := provider.AuthenticateExecution(ctx, expected)
		if !errors.Is(err, errChildPending) {
			return receipt, err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, contextError(ctx)
		case <-timer.C:
		}
	}
}

// AuthenticateExecution authenticates an exact active or terminal child without
// turning a failed or running task into a successful artifact receipt.
func (provider *Provider) AuthenticateExecution(ctx context.Context, expected TaskExpectation) (*TaskReceipt, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if provider == nil || provider.client == nil || !resourceName.MatchString(expected.Name) ||
		!resourceName.MatchString(expected.RunName) || !resourceUID.MatchString(expected.RunUID) ||
		!resourceName.MatchString(expected.ServiceAccount) {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton task requires exact task, TaskRun, UID and service account")
	}
	wantSpec, err := exactJSON(expected.Spec)
	if err != nil {
		return nil, err
	}
	var run pipelineRun
	if err := provider.get(ctx, "pipelineruns", provider.policy.RunName, &run); err != nil {
		return nil, err
	}
	if run.APIVersion != "tekton.dev/v1" || run.Kind != "PipelineRun" ||
		!provider.matches(run.Metadata, provider.policy.RunName, provider.policy.RunUID) || run.Spec.Status != "" {
		return nil, e.New(e.CodeVerifyFailed, "Tekton PipelineRun identity changed or execution was canceled")
	}
	actualSpec, err := exactJSON(run.exactSpec)
	if err != nil || !bytes.Equal(actualSpec, provider.policy.RunSpec) {
		return nil, e.New(e.CodePipelineDrift, "Tekton PipelineRun differs from the admitted embedded specification")
	}
	var specification struct {
		Tasks   []pipelineTask `json:"tasks"`
		Finally []pipelineTask `json:"finally"`
	}
	if err := json.Unmarshal(run.Spec.PipelineSpec, &specification); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "reading Tekton PipelineSpec")
	}
	found := 0
	var selected pipelineTask
	for _, task := range append(specification.Tasks, specification.Finally...) {
		if task.Name == expected.Name {
			found++
			selected = task
			spec, err := EmbeddedTaskSpec(task.TaskSpec)
			if err != nil || !bytes.Equal(spec, wantSpec) {
				return nil, e.New(e.CodePipelineDrift, "Tekton task differs from its admitted PipelineSpec")
			}
		}
	}
	if found != 1 {
		return nil, e.New(e.CodePipelineDrift, "Tekton PipelineSpec must contain exactly one selected task")
	}
	found = 0
	for _, child := range run.Status.ChildReferences {
		if child.PipelineTaskName == expected.Name {
			found++
			if child.APIVersion != "tekton.dev/v1" || child.Kind != "TaskRun" || child.Name != expected.RunName {
				return nil, e.New(e.CodeVerifyFailed, "Tekton task is not the controller-recorded child execution")
			}
		}
	}
	if found == 0 {
		return nil, e.Wrap(e.CodeVerifyFailed, errChildPending, "Tekton Run omits the selected child execution")
	}
	if found != 1 {
		return nil, e.New(e.CodeVerifyFailed, "Tekton Run repeats the selected child execution")
	}
	var task taskRun
	if err := provider.get(ctx, "taskruns", expected.RunName, &task); err != nil {
		return nil, err
	}
	if task.APIVersion != "tekton.dev/v1" || task.Kind != "TaskRun" ||
		!provider.matches(task.Metadata, expected.RunName, expected.RunUID) ||
		task.Metadata.Labels["tekton.dev/pipelineTask"] != expected.Name ||
		task.Metadata.Labels["tekton.dev/pipelineRun"] != provider.policy.RunName ||
		task.Spec.ServiceAccountName != expected.ServiceAccount || task.Spec.Status != "" || len(task.Status.Retries) != 0 {
		return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun does not match its admitted execution")
	}
	owners := task.Metadata.OwnerReferences
	if len(owners) != 1 || owners[0].APIVersion != "tekton.dev/v1" || owners[0].Kind != "PipelineRun" ||
		owners[0].Name != provider.policy.RunName || owners[0].UID != provider.policy.RunUID || !owners[0].Controller {
		return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun has a foreign or ambiguous controller owner")
	}
	actualTask, err := exactJSON(task.Spec.TaskSpec)
	if err != nil || !bytes.Equal(actualTask, wantSpec) {
		return nil, e.New(e.CodePipelineDrift, "Tekton TaskRun substituted the embedded TaskSpec")
	}
	params := map[string]string{"run-name": provider.policy.RunName, "run-uid": provider.policy.RunUID}
	if len(task.Spec.Params) != len(params) {
		return nil, e.New(e.CodePipelineDrift, "Tekton TaskRun requires its exact invocation parameters")
	}
	for _, param := range task.Spec.Params {
		want, ok := params[param.Name]
		if !ok || param.Value != want {
			return nil, e.New(e.CodePipelineDrift, "Tekton TaskRun substituted or repeated an invocation parameter")
		}
		delete(params, param.Name)
	}
	conditions := task.Status.Conditions
	if err := provider.authenticateTaskSpec(ctx, run, task, selected, expected); err != nil {
		return nil, err
	}
	if len(conditions) != 1 || conditions[0].Type != "Succeeded" {
		return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun has ambiguous execution status")
	}
	state := ""
	switch conditions[0].Status {
	case "True":
		if conditions[0].Reason != "Succeeded" {
			return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun has an unsupported success reason")
		}
		state = "success"
	case "False":
		state = "failure"
	case "Unknown":
		state = "running"
	default:
		return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun has an unsupported execution status")
	}
	if state != "running" {
		_, err = time.Parse(time.RFC3339Nano, task.Status.CompletionTime)
		if err != nil {
			return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun completion time is invalid")
		}
	} else if task.Status.CompletionTime != "" {
		return nil, e.New(e.CodeVerifyFailed, "Tekton active task carries terminal state")
	}
	if len(task.Status.Results) > 16 {
		return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun result count exceeds its bound")
	}
	results := make(map[string]string, len(task.Status.Results))
	for _, result := range task.Status.Results {
		if _, exists := results[result.Name]; exists || !resourceName.MatchString(result.Name) || len(result.Value) > 4096 {
			return nil, e.New(e.CodeVerifyFailed, "Tekton TaskRun has repeated, invalid or oversized results")
		}
		results[result.Name] = result.Value
	}
	return &TaskReceipt{invocation: provider.policy.Server + "/apis/tekton.dev/v1/namespaces/" +
		provider.policy.Namespace + "/pipelineruns/" + provider.policy.RunName + "?uid=" + provider.policy.RunUID,
		builder: provider.policy.Server + "/api/v1/namespaces/" + provider.policy.Namespace + "/serviceaccounts/nvkr-publisher",
		taskUID: expected.RunUID, planDigest: provider.policy.PlanDigest, account: expected.ServiceAccount, state: state, results: results}, nil
}

func (provider *Provider) matches(metadata objectMetadata, name, uid string) bool {
	return metadata.Name == name && metadata.UID == uid && metadata.Namespace == provider.policy.Namespace &&
		metadata.Annotations[PlanAnnotation] == provider.policy.PlanDigest && metadata.DeletionTimestamp == ""
}

func (provider *Provider) get(ctx context.Context, resource, name string, output any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	api := "/apis/tekton.dev/v1/namespaces/"
	if resource == "persistentvolumeclaims" {
		api = "/api/v1/namespaces/"
	}
	endpoint := provider.policy.Server + api + provider.policy.Namespace + "/" + resource + "/" + name
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "constructing Tekton API request")
	}
	request.Header.Set("Authorization", "Bearer "+provider.policy.Token)
	request.Header.Set("Accept", "application/json")
	response, err := provider.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "Tekton API request canceled")
		}
		return e.Wrap(e.CodeFetchFailed, err, "authenticating Tekton execution")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return e.New(e.CodeFetchFailed, "Tekton API returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAPIBytes+1))
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "reading Tekton API response")
	}
	if len(raw) > maxAPIBytes {
		return e.New(e.CodeVerifyFailed, "Tekton API response exceeds its byte bound")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "invalid Tekton API JSON")
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "invalid Tekton API object")
	}
	switch output := output.(type) {
	case *pipelineRun:
		var document struct {
			Spec json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading exact Tekton execution specification")
		}
		output.exactSpec = bytes.Clone(document.Spec)
	case *taskRun:
		var document struct {
			Spec json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading exact Tekton task specification")
		}
		output.exactSpec = bytes.Clone(document.Spec)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "Tekton operation requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "Tekton operation canceled")
	}
	return nil
}

func exactJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxAPIBytes || raw[0] != '{' {
		return nil, e.New(e.CodePipelineDrift, "Tekton requires a bounded embedded object specification")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "invalid Tekton specification")
	}
	return canonical.Marshal(raw)
}

// EmbeddedTaskSpec removes only v1.18's inert EmbeddedTask wrapper defaults.
// Nonempty metadata and nested custom-task specifications are unsupported.
func EmbeddedTaskSpec(raw json.RawMessage) ([]byte, error) {
	if _, err := exactJSON(raw); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decoding embedded Tekton task")
	}
	if metadata, ok := fields["metadata"]; ok {
		canonical, err := exactJSON(metadata)
		if err != nil || string(canonical) != "{}" {
			return nil, e.New(e.CodePipelineDrift, "Tekton embedded task metadata overrides are unsupported")
		}
		delete(fields, "metadata")
	}
	if spec, ok := fields["spec"]; ok {
		if string(bytes.TrimSpace(spec)) != "null" {
			return nil, e.New(e.CodePipelineDrift, "Tekton custom task specifications are unsupported")
		}
		delete(fields, "spec")
	}
	return canonical.Marshal(fields)
}

type objectMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	DeletionTimestamp string            `json:"deletionTimestamp,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	OwnerReferences   []controllerOwner `json:"ownerReferences,omitempty"`
}

type controllerOwner struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller bool   `json:"controller"`
}

type childReference struct {
	APIVersion       string `json:"apiVersion"`
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	PipelineTaskName string `json:"pipelineTaskName"`
}

type nameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type pipelineTask struct {
	Name       string          `json:"name"`
	TaskSpec   json.RawMessage `json:"taskSpec"`
	Timeout    string          `json:"timeout,omitempty"`
	Workspaces []struct {
		Name      string `json:"name"`
		Workspace string `json:"workspace"`
		SubPath   string `json:"subPath,omitempty"`
	} `json:"workspaces,omitempty"`
}

type pipelineRun struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   objectMetadata `json:"metadata"`
	Spec       struct {
		PipelineSpec    json.RawMessage `json:"pipelineSpec"`
		TaskRunTemplate json.RawMessage `json:"taskRunTemplate,omitempty"`
		Status          string          `json:"status,omitempty"`
	} `json:"spec"`
	Status struct {
		ChildReferences []childReference `json:"childReferences,omitempty"`
	} `json:"status"`
	exactSpec json.RawMessage
}

type taskRun struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   objectMetadata `json:"metadata"`
	Spec       struct {
		TaskSpec           json.RawMessage `json:"taskSpec"`
		ServiceAccountName string          `json:"serviceAccountName"`
		Status             string          `json:"status,omitempty"`
		Params             []nameValue     `json:"params"`
		Timeout            string          `json:"timeout,omitempty"`
		PodTemplate        json.RawMessage `json:"podTemplate,omitempty"`
		Workspaces         json.RawMessage `json:"workspaces,omitempty"`
	} `json:"spec"`
	Status struct {
		Conditions     []condition       `json:"conditions"`
		CompletionTime string            `json:"completionTime,omitempty"`
		Results        []nameValue       `json:"results,omitempty"`
		Retries        []json.RawMessage `json:"retriesStatus,omitempty"`
	} `json:"status"`
	exactSpec json.RawMessage
}
