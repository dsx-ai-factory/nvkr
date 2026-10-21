// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/kubeowned"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

const maxReportBytes = 64 << 20
const JUnitProfile = "kubernetes-e2e-ginkgo"

// Producer is the configured upstream Hydrophone adapter. Its successful return
// means orchestration and raw capture completed, not that JUnit is valid or tests
// passed. Its binding describes authenticated source inputs and selected API image assertions, not independent installed-byte or qualification authority.
type Producer struct {
	options     Options
	captureTool executableCapture
	local       *localSources
}

var _ testresults.Producer = (*Producer)(nil)

// New validates configuration and the borrowed artifact receipt. Produce authenticates executable bytes and
// the kubeconfig before cluster I/O. Caller environment is never inherited.
func New(options Options) (*Producer, error) {
	options.localDiagnostic = false
	options, err := validateOptions(options)
	if err != nil {
		return nil, err
	}
	description, err := options.Artifacts.Description()
	if err != nil {
		return nil, err
	}
	if _, err = selectOutputs(description, options.Targets); err != nil {
		return nil, err
	}
	tools, err := options.ToolArtifacts.Description()
	if err != nil {
		return nil, err
	}
	if err = validateToolSelections(tools, options); err != nil {
		return nil, err
	}
	if _, _, err = resolveImages(tools, options); err != nil {
		return nil, err
	}
	return &Producer{options: options}, nil
}
func (p *Producer) Produce(ctx context.Context) (output testresults.Output, resultErr error) {
	defer func() {
		if resultErr != nil && output.SchemaVersion != "" {
			output.CaptureStatus = testresults.CapturePartial
		}
	}()
	if ctx == nil {
		return testresults.Output{}, e.New(e.CodeUsage, "conformance requires context")
	}
	if err := ctx.Err(); err != nil {
		return testresults.Output{}, e.Wrap(e.CodeTimeout, err, "conformance canceled before admission")
	}
	if p == nil {
		return testresults.Output{}, e.New(e.CodeUsage, "configured conformance producer required")
	}
	if p.local != nil {
		return p.produceLocal(ctx)
	}
	err := p.options.ToolArtifacts.WithDiagnosticOperation(ctx, func(toolsContext context.Context, tools lifecycle.ArtifactCheckDescription) error {
		if err := validateToolSelections(tools, p.options); err != nil {
			return err
		}
		if _, _, err := resolveImages(tools, p.options); err != nil {
			return err
		}
		return p.options.Artifacts.WithDiagnosticOperation(toolsContext, func(operation context.Context, description lifecycle.ArtifactCheckDescription) error {
			var err error
			output, err = p.produce(operation, description, tools)
			return err
		})
	})
	return output, err
}

// produce is private: only the public receipt-owned operation admits production use.
func (p *Producer) produce(ctx context.Context, description, tools lifecycle.ArtifactCheckDescription) (output testresults.Output, resultErr error) {
	defer func() {
		if resultErr != nil && output.SchemaVersion != "" {
			output.CaptureStatus = testresults.CapturePartial
		}
	}()
	outputs, err := selectOutputs(description, p.options.Targets)
	if err != nil {
		return testresults.Output{}, err
	}
	if err = validateToolSelections(tools, p.options); err != nil {
		return testresults.Output{}, err
	}
	image, busybox, err := resolveImages(tools, p.options)
	if err != nil {
		return testresults.Output{}, err
	}
	a, err := admit(ctx, p.options)
	if err != nil {
		return testresults.Output{}, err
	}
	defer func() { a.close(); resultErr = errors.Join(resultErr, a.closeNative()) }()
	capture := p.captureTool
	if capture == nil {
		capture = func(ctx context.Context, selector ExecutableSelector) (*heldfile.File, error) {
			if p.local != nil {
				return captureExecutable(ctx, selector, p.local.tools.WriteExecutableMember)
			}
			return captureExecutable(ctx, selector, p.options.ToolArtifacts.WriteExecutableMember)
		}
	}
	a.hydrophone, err = capture(ctx, p.options.Hydrophone)
	if err != nil {
		return testresults.Output{}, err
	}
	a.kubectl, err = capture(ctx, p.options.Kubectl)
	if err != nil {
		return testresults.Output{}, err
	}
	a.options.Image, a.options.BusyboxImage = image, busybox
	o := a.options
	root, err := filepath.Abs(o.WorkspaceRoot)
	if err != nil {
		return testresults.Output{}, e.Wrap(e.CodeFetchFailed, err, "resolving private conformance workspace")
	}
	workspace, err := os.MkdirTemp(root, "nvkr-conformance-")
	if err != nil {
		return testresults.Output{}, e.Wrap(e.CodeFetchFailed, err, "creating private conformance workspace")
	}
	output = testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CapturePartial,
		Producer: testresults.ProducerIdentity{Name: "nvkr-kubernetes-conformance", Revision: testresults.Metadata{Status: "unknown"}},
		Suite:    testresults.SuiteSelection{Name: o.SuiteName, Selection: []string{`\[Conformance\]`}, Revision: testresults.Metadata{Status: "declared", Value: o.Image}},
		Harness:  &testresults.HarnessIdentity{Name: "hydrophone", Version: testresults.Metadata{Status: "unknown"}},
	}
	facts := append(publicationFacts("sourceRun", description), publicationFacts("toolsSource", tools)...)
	if p.local != nil {
		facts = append(localSourceFacts("sourceRun", p.local.productDescription), localSourceFacts("toolsSource", p.local.toolsDescription)...)
	}
	for _, tool := range []struct {
		name     string
		selector ExecutableSelector
	}{{"hydrophone", o.Hydrophone}, {"kubectl", o.Kubectl}} {
		for _, field := range []struct{ name, value string }{{"output", tool.selector.Output}, {"member", tool.selector.Member}, {"platform", tool.selector.Platform}} {
			facts = append(facts, testresults.EnvironmentFact{Scope: "toolsSource", Name: tool.name + "." + field.name, Phase: "before", Metadata: testresults.Metadata{Status: "observed", Value: field.value}})
		}
	}
	if err = appendProjection(&output, facts); err != nil {
		return output, err
	}
	before, err := a.observe(ctx, workspace, "before")
	projectionErr := appendProjection(&output, before)
	if projectionErr != nil {
		return output, errors.Join(err, projectionErr)
	}

	if err != nil {
		return output, err
	}
	if err = admitProjectionCount(len(before), len(facts), o.Targets); err != nil {
		return output, err
	}
	baseline, subjects, err := a.observeDeployments(ctx, workspace, outputs)
	if err != nil {
		return output, err
	}
	inputs := description.Inputs
	output.Binding = &testresults.Binding{Inputs: &inputs, Subjects: subjects}
	if p.local != nil {
		output.Binding.Inputs = nil
	}
	if err = appendProjection(&output, deploymentFacts(baseline, "before")); err != nil {
		return output, err
	}
	if err = appendProjection(&output, []testresults.EnvironmentFact{{Scope: "cluster", Name: "disposableCluster.acknowledged", Phase: "before", Metadata: testresults.Metadata{Status: "declared", Value: "true"}}}); err != nil {
		return output, err
	}
	if err = admitTerminalProjection(output, before, baseline, filepath.Join(workspace, "original-junit_01.xml")); err != nil {
		return output, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return output, e.Wrap(e.CodeInternal, err, "generating unique conformance namespace")
	}
	namespace := "nvkr-conformance-" + hex.EncodeToString(random[:])
	refs := resourceRefs(namespace)
	for _, ref := range refs {
		found, err := a.getObject(ctx, workspace, ref)
		if err != nil {
			return output, err
		}
		if found.Metadata.UID != "" {
			return output, failure("unique conformance attempt resource already exists")
		}
	}
	raw, _, err := a.kube(ctx, workspace, nil, "create", "namespace", namespace, "-o", "json")
	if err != nil {
		return output, err
	}
	var ns object
	if err = decodeAPI(raw, &ns); err != nil || ns.Metadata.UID == "" {
		return output, failure("created namespace UID unavailable; cleanup ownership not established")
	}
	owned := map[string]string{refs[0].path: ns.Metadata.UID}
	args := []string{"--conformance", "--disable-progress-status", "--namespace=" + namespace, "--skip-preflight=" + namespace, "--conformance-image=" + o.Image, "--busybox-image=" + o.BusyboxImage, "--output-dir=" + workspace, "--parallel=1", "--verbosity=1", "--startup-timeout=" + o.StartupTimeout.String()}
	_, exitCode, runErr := a.command(ctx, a.hydrophone, args, workspace, nil, o.RunTimeout)
	exitStatus := testresults.Metadata{Status: "unavailable"}
	if exitCode >= 0 {
		exitStatus = testresults.Metadata{Status: "observed", Value: strconv.Itoa(exitCode)}
	}
	exitProjectionErr := appendProjection(&output, []testresults.EnvironmentFact{{Scope: "harness", Name: "process.exitCode", Phase: "after", Metadata: exitStatus}})
	// Recovery and cleanup own bounded contexts after a canceled runner is joined.
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), o.CommandTimeout)
	defer recoveryCancel()
	rawPath := filepath.Join(workspace, "junit_01.xml")
	held, readErr := heldfile.OpenContext(recoveryCtx, rawPath, heldfile.Options{Label: "original conformance JUnit", MaxBytes: maxReportBytes})
	var report []byte
	if readErr == nil {
		report, readErr = held.ReadAllContext(recoveryCtx, maxReportBytes)
		_ = held.Close()
	}
	if readErr != nil || len(report) == 0 {
		report, _, readErr = a.kubeResults(recoveryCtx, workspace, "exec", "--namespace="+namespace, "e2e-conformance-test", "--container=output-container", "--", "cat", "/tmp/results/junit_01.xml")
	}
	if readErr == nil && len(report) == 0 {
		readErr = failure("original suite results are empty")
	}
	if readErr == nil {
		captured := filepath.Join(workspace, "original-junit_01.xml")
		readErr = saveOriginal(recoveryCtx, captured, report)
		if readErr == nil {
			output.Reports = []testresults.ReportReference{{Format: testresults.FormatJUnit, Profile: JUnitProfile, Reference: captured, Digest: blobDigest(report), ByteLength: int64(len(report))}}
		}
	} else {
		readErr = captureFailure(recoveryCtx, readErr, e.CodeVerifyFailed, "original suite results unavailable")
	}
	recoveryCancel()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), o.CleanupTimeout)
	defer cancel()
	cleanupErr := a.cleanup(cleanupCtx, workspace, refs, owned, namespace)
	afterCtx, afterCancel := context.WithTimeout(context.Background(), afterObservationTimeout(o))
	defer afterCancel()
	after, afterErr := a.observe(afterCtx, workspace, "after")
	afterProjectionErr := appendProjection(&output, after)
	terminal, _, deploymentErr := a.observeDeployments(afterCtx, workspace, outputs)
	if deploymentErr == nil {
		deploymentErr = compareDeployments(baseline, terminal)
	}
	deploymentProjectionErr := appendProjection(&output, deploymentFacts(terminal, "after"))
	sort.Slice(output.Environment, func(i, j int) bool {
		a, b := output.Environment[i], output.Environment[j]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		return a.Name < b.Name
	})
	if err := errors.Join(runErr, exitProjectionErr, cleanupErr, afterErr, afterProjectionErr, deploymentErr, deploymentProjectionErr, readErr); err != nil {
		return output, err
	}
	output.CaptureStatus = testresults.CaptureComplete
	_, err = testresults.Encode(output)
	return output, err
}

func publicationFacts(scope string, description lifecycle.ArtifactCheckDescription) []testresults.EnvironmentFact {
	facts := make([]testresults.EnvironmentFact, 0, 5)
	for _, fact := range []struct{ name, value string }{{"publication", description.Inputs.ReleaseSubject}, {"invocationID", description.Origin.InvocationID}, {"commit", description.Origin.Commit}, {"repository", description.Origin.Repository}, {"workflow", description.Origin.Workflow}} {
		metadata := testresults.Metadata{Status: "unavailable"}
		if fact.value != "" {
			metadata = testresults.Metadata{Status: "observed", Value: fact.value}
		}
		facts = append(facts, testresults.EnvironmentFact{Scope: scope, Name: fact.name, Phase: "before", Metadata: metadata})
	}
	return facts
}

func afterObservationTimeout(options Options) time.Duration {
	// observe uses version/nodes/CNI calls; deployment observation uses nodes,
	// cluster UID, and one call per target. Each retains its own command timeout.
	return min(time.Duration(len(options.Targets)+5)*options.CommandTimeout, 6*time.Hour)
}

func saveOriginal(ctx context.Context, path string, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "result capture canceled")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "retaining original JUnit")
	}
	defer file.Close()
	for remaining := raw; len(remaining) > 0; {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "result capture canceled")
		}
		count := min(len(remaining), 64<<10)
		n, err := file.Write(remaining[:count])
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "retaining original JUnit")
		}
		if n != count {
			return failure("incomplete original JUnit capture")
		}
		remaining = remaining[count:]
	}
	if err := file.Close(); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "closing original JUnit capture")
	}
	return nil
}

func blobDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type resourceRef struct{ path string }
type object struct {
	Metadata struct {
		UID               string  `json:"uid"`
		DeletionTimestamp *string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Subjects []struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		APIGroup  string `json:"apiGroup"`
	} `json:"subjects"`
	RoleRef struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		APIGroup string `json:"apiGroup"`
	} `json:"roleRef"`
}

func resourceRefs(ns string) []resourceRef {
	return []resourceRef{{"/api/v1/namespaces/" + ns}, {"/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/conformance-serviceaccount-role:" + ns}, {"/apis/rbac.authorization.k8s.io/v1/clusterroles/conformance-serviceaccount:" + ns}}
}
func (a *admitted) getObject(ctx context.Context, workspace string, ref resourceRef) (object, error) {
	// --ignore-not-found distinguishes absence from authentication/network errors
	// without interpreting localized stderr or accepting an arbitrary exit code.
	kind, name := path.Base(path.Dir(ref.path)), path.Base(ref.path)
	raw, _, err := a.kube(ctx, workspace, nil, "get", kind, name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return object{}, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return object{}, nil
	}
	var out object
	if err = decodeAPI(raw, &out); err != nil {
		return object{}, err
	}
	if out.Metadata.UID == "" {
		return object{}, failure("resource UID unavailable")
	}
	return out, nil
}
func (a *admitted) cleanup(ctx context.Context, workspace string, refs []resourceRef, owned map[string]string, namespace string) error {
	// Capture identities before deletion. A replaced namespace fails closed.
	for _, ref := range refs {
		found, err := a.getObject(ctx, workspace, ref)
		if err != nil {
			return err
		}
		if found.Metadata.UID == "" {
			continue
		}
		if uid := owned[ref.path]; uid != "" && uid != found.Metadata.UID {
			return failure("attempt namespace replaced; cleanup rejected")
		}
		if ref.path == refs[1].path {
			if len(found.Subjects) != 1 || found.Subjects[0].Namespace != namespace || found.Subjects[0].Name != "conformance-serviceaccount" || found.Subjects[0].Kind != "ServiceAccount" || found.Subjects[0].APIGroup != "" || found.RoleRef.Name != "conformance-serviceaccount:"+namespace || found.RoleRef.Kind != "ClusterRole" || found.RoleRef.APIGroup != "rbac.authorization.k8s.io" {
				return failure("attempt role binding has unrelated subjects")
			}
		}
		owned[ref.path] = found.Metadata.UID
	}
	return cleanupDeleteLoop(ctx, refs, owned,
		func(ctx context.Context, ref resourceRef) (object, error) { return a.getObject(ctx, workspace, ref) },
		func(ctx context.Context, ref resourceRef, body []byte) error {
			_, _, err := a.kube(ctx, workspace, bytes.NewReader(body), "delete", "--raw="+ref.path, "--filename=-")
			return err
		}, time.Second)
}

// Keep the first deletion cause before subsequent GET or deadline errors. The
// kubectl owner does not parse localized stderr. A fresh GET can prove absence
// after a DELETE race; otherwise an unclassified refusal is permanent. Explicitly
// coded unavailability/timeouts can be retried within the cleanup deadline.
func cleanupDeleteLoop(ctx context.Context, refs []resourceRef, owned map[string]string,
	get func(context.Context, resourceRef) (object, error),
	deleteObject func(context.Context, resourceRef, []byte) error, interval time.Duration) error {
	// Preserve the existing RBAC-before-namespace order and all UID bindings.
	bindings := make([]kubeowned.Binding, 0, len(refs))
	for _, ref := range []resourceRef{refs[1], refs[2], refs[0]} {
		bindings = append(bindings, kubeowned.Binding{Path: ref.path, UID: owned[ref.path]})
	}
	return kubeowned.Delete(ctx, bindings, func(ctx context.Context, resource string) (kubeowned.Observation, error) {
		found, err := get(ctx, resourceRef{path: resource})
		return kubeowned.Observation{UID: found.Metadata.UID, Deleting: found.Metadata.DeletionTimestamp != nil}, err
	}, func(ctx context.Context, resource string, body []byte) error {
		return deleteObject(ctx, resourceRef{path: resource}, body)
	}, interval)
}
