// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/tekton"
)

// TektonTaskOptions identifies the exact provider invocation. These fields are
// descriptions until the selected Kubernetes API authenticates their binding.
type TektonTaskOptions struct {
	Operation, Stage, PlanParts, Execution string
	RunName, RunUID, TaskRun, TaskUID      string
	Outcome, CarrierDigest                 string
	Stdout, Stderr                         io.Writer
}

type tektonRuntimeTask struct {
	spec    json.RawMessage
	account string
}
type tektonRuntime struct {
	plan       *Plan
	projection *tektonProjection
	provider   *tekton.Provider
	tasks      map[string]tektonRuntimeTask
	options    TektonTaskOptions
}

// ExecuteTektonTaskWithEvidence delegates evidence policy to the lifecycle owner
// after the factory authenticates the exact task. The callback cannot mint a
// scope or change the task's independently rendered operation or credentials.
// validatePlan applies that owner's static smoke scope before any producer work.
func ExecuteTektonTaskWithEvidence(ctx context.Context, options TektonTaskOptions, validatePlan func(*Plan) error, evidence func(context.Context, *TektonEvidenceTask) error) error {
	if err := executionContext(ctx); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "Tekton execution requires native Linux")
	}
	if validatePlan == nil || evidence == nil {
		return e.New(e.CodeEngineCapability, "Tekton requires the lifecycle admission and evidence owners")
	}
	if options.PlanParts != "/nvkr/plan" || options.Execution != "/nvkr/execution" || options.Outcome != "/tekton/results/outcome" || options.CarrierDigest != "/tekton/results/carrier" {
		return e.New(e.CodeEngineCapability, "Tekton task requires its generated control and result mounts")
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	runner, err := openTektonRuntime(ctx, options)
	if err != nil {
		return err
	}
	defer runner.provider.Close()
	if err := validatePlan(runner.plan); err != nil {
		return err
	}
	selected, ok := runner.tasks[options.Stage]
	if !ok {
		return e.New(e.CodeEngineCapability, "unknown Tekton execution stage")
	}
	receipt, err := runner.provider.AwaitExecution(ctx, tekton.TaskExpectation{Name: options.Stage, RunName: options.TaskRun, RunUID: options.TaskUID, ServiceAccount: selected.account, Spec: selected.spec})
	if err != nil {
		return err
	}
	if receipt.State() != "running" {
		return e.New(e.CodeVerifyFailed, "Tekton task is not an active provider execution")
	}
	operation, err := tektonTaskOperation(selected.spec)
	if err != nil {
		return err
	}
	if operation != options.Operation {
		return e.New(e.CodePipelineDrift, "Tekton command differs from its admitted operation")
	}
	runner.plan.tektonExecution = receipt
	return runner.withActiveTask(ctx, func(ctx context.Context) error {
		switch options.Operation {
		case "prepare":
			for _, stage := range sortedKeys(runner.tasks) {
				if stage == tektonPrepare {
					continue
				}
				if err := os.Mkdir(filepath.Join("/nvkr/output", stage), 0700); err != nil {
					return e.Wrap(e.CodeImmutableViolation, err, "preparing fresh Tekton carrier directory")
				}
			}
			return runner.complete(ctx, nil)
		case "execute":
			return runner.execute(ctx)
		case "smoke", "qualify", "finalize":
			if evidence == nil {
				return e.New(e.CodeEngineCapability, "Tekton requires the lifecycle evidence owner")
			}
			scope := &TektonEvidenceTask{runner: runner}
			defer func() { scope.closed.Store(true) }()
			if err := scope.prepare(ctx); err != nil {
				return err
			}
			if err := evidence(ctx, scope); err != nil {
				return err
			}
			if err := scope.Require(ctx); err != nil {
				return err
			}
			return runner.complete(ctx, runner.outputs(options.Stage))
		default:
			return e.New(e.CodeEngineCapability, "unknown Tekton operation")
		}
	})
}

// Keep the provider lease live during long builds, not only between nodes.
// Cancellation or loss of provider authentication cancels subprocess contexts;
// the owner joins this watcher before returning or closing the API transport.
func (runner *tektonRuntime) withActiveTask(ctx context.Context, operation func(context.Context) error) error {
	return watchTektonTask(ctx, 2*time.Second, func(ctx context.Context) error {
		receipt, err := runner.authenticate(ctx, runner.options.Stage)
		if err == nil && receipt.State() != "running" {
			return e.New(e.CodeVerifyFailed, "Tekton execution ceased to be active")
		}
		return err
	}, operation)
}

func watchTektonTask(ctx context.Context, interval time.Duration, authenticate func(context.Context) error, operation func(context.Context) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := authenticate(ctx)
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	err := operation(ctx)
	finished := errors.New("tekton operation completed")
	cancel(finished)
	<-done
	cause := context.Cause(ctx)
	if cause == finished {
		cause = nil
	}
	return errors.Join(err, cause)
}

func openTektonRuntime(ctx context.Context, options TektonTaskOptions) (*tektonRuntime, error) {
	var raw []byte
	for index := 0; index < int(MaxPlanBytes)/tektonChunkBytes; index++ {
		part, err := readTektonControl(ctx, filepath.Join(options.PlanParts, fmt.Sprintf("%03d", index)), tektonChunkBytes)
		if os.IsNotExist(err) && index > 0 {
			break
		}
		if err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading Tekton Plan part")
		}
		raw = append(raw, part...)
		if len(part) < tektonChunkBytes {
			break
		}
	}
	var plan Plan
	if err := canonical.UnmarshalExact(raw, &plan); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decoding Tekton Plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if plan.Environment.Tekton == nil || plan.Environment.Tekton.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		return nil, e.New(e.CodeEngineCapability, "Tekton task differs from its selected native platform")
	}
	projection, err := projectTektonExecution(ctx, &plan, plan.Environment.Tekton.Platform)
	if err != nil {
		return nil, err
	}
	bundle, err := renderTekton(ctx, &plan)
	if err != nil {
		return nil, err
	}
	var resources struct {
		Items []struct {
			Kind string          `json:"kind"`
			Spec json.RawMessage `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(bundle.Files[tektonResourcePath], &resources); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "decoding core Tekton projection")
	}
	var expected json.RawMessage
	for _, item := range resources.Items {
		if item.Kind == "PipelineRun" {
			if expected != nil {
				return nil, e.New(e.CodeInternal, "Tekton projection repeats PipelineRun")
			}
			expected = item.Spec
		}
	}
	actual, err := readTektonControl(ctx, filepath.Join(options.Execution, "run.json"), 768<<10)
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "reading Tekton execution binding")
	}
	if !bytes.Equal(expected, actual) {
		return nil, e.New(e.CodePipelineDrift, "Tekton mounted execution differs from its complete Plan projection")
	}
	var spec struct {
		PipelineSpec struct {
			Tasks, Finally []struct {
				Name     string
				TaskSpec json.RawMessage
			}
		} `json:"pipelineSpec"`
		TaskRunSpecs []struct{ PipelineTaskName, ServiceAccountName string } `json:"taskRunSpecs"`
	}
	if err := json.Unmarshal(expected, &spec); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "decoding Tekton execution")
	}
	tasks := map[string]tektonRuntimeTask{}
	accounts := map[string]string{}
	for _, entry := range spec.TaskRunSpecs {
		accounts[entry.PipelineTaskName] = entry.ServiceAccountName
	}
	for _, task := range append(spec.PipelineSpec.Tasks, spec.PipelineSpec.Finally...) {
		taskSpec, err := tekton.EmbeddedTaskSpec(task.TaskSpec)
		if err != nil {
			return nil, err
		}
		tasks[task.Name] = tektonRuntimeTask{taskSpec, accounts[task.Name]}
	}
	token, err := readTektonControl(ctx, "/nvkr/api/token", 64<<10)
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "reading scoped Tekton API token")
	}
	settings := plan.Environment.Tekton
	provider, err := tekton.NewProvider(tekton.ProviderPolicy{Server: settings.APIServer, Namespace: settings.Namespace, RunName: options.RunName, RunUID: options.RunUID, PlanDigest: bundle.PlanDigest, CertificateAuthority: []byte(settings.CertificateAuthority), RunSpec: expected, Token: strings.TrimSpace(string(token))})
	clear(token)
	if err != nil {
		return nil, err
	}
	return &tektonRuntime{&plan, projection, provider, tasks, options}, nil
}

// Projected ConfigMap and Secret mounts use Kubernetes-managed symlinks. Read
// bounded snapshots; complete Plan/spec authentication follows before work.
func readTektonControl(ctx context.Context, path string, limit int) ([]byte, error) {
	if err := executionContext(ctx); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, e.New(e.CodeVerifyFailed, "Tekton control is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, e.New(e.CodeVerifyFailed, "Tekton control exceeds its byte bound")
	}
	return raw, executionContext(ctx)
}

func tektonTaskOperation(raw json.RawMessage) (string, error) {
	var spec struct{ Steps []struct{ Args []string } }
	if err := json.Unmarshal(raw, &spec); err != nil || len(spec.Steps) != 1 || len(spec.Steps[0].Args) < 3 {
		return "", e.New(e.CodePipelineDrift, "Tekton task has no exact runner invocation")
	}
	return spec.Steps[0].Args[2], nil
}

func (runner *tektonRuntime) authenticate(ctx context.Context, id string) (*tekton.TaskReceipt, error) {
	task, ok := runner.tasks[id]
	if !ok {
		return nil, e.New(e.CodePipelineDrift, "unknown Tekton prerequisite")
	}
	return runner.provider.AuthenticateStage(ctx, id, task.account, task.spec)
}

func (runner *tektonRuntime) execute(ctx context.Context) error {
	var selected *githubStage
	for index := range runner.projection.stages {
		if runner.projection.stages[index].ID == runner.options.Stage {
			selected = &runner.projection.stages[index]
		}
	}
	if selected == nil {
		return e.New(e.CodePipelineDrift, "Tekton execution has no projected stage")
	}
	for _, id := range runner.projection.dependencies[selected.ID] {
		receipt, err := runner.authenticate(ctx, id)
		if err != nil {
			return err
		}
		if receipt.State() != "success" && selected.executionCondition() == executionAfterSuccess {
			return e.New(e.CodeReleaseIncomplete, "Tekton prerequisite %q did not succeed", id)
		}
	}
	_, artifacts := planIndex(runner.plan)
	inputs, err := selected.carrierInputs(runner.plan, artifacts, runner.projection.owners)
	if err != nil {
		return err
	}
	byStage := map[string][]string{}
	for _, id := range inputs {
		owner := runner.projection.owners[artifacts[id].Producer]
		byStage[owner] = append(byStage[owner], id)
	}
	for _, owner := range sortedKeys(byStage) {
		if err := runner.restore(ctx, owner, byStage[owner]); err != nil {
			return err
		}
	}
	for _, node := range selected.Nodes {
		// Reauthenticate before each effect; a canceled Run cannot admit later work.
		receipt, err := runner.authenticate(ctx, selected.ID)
		if err != nil {
			return err
		}
		if receipt.State() != "running" {
			return e.New(e.CodeVerifyFailed, "Tekton stage ceased to be active")
		}
		runner.plan.tektonExecution = receipt
		if node.Kind == NodeFetch && irProjectedNode(node) {
			if err := executeSourceFetch(ctx, runner.plan, node.ID, os.Environ(), runner.options.Stdout, runner.options.Stderr, (pipeline.Local{}).ExecNode); err != nil {
				return err
			}
			continue
		}
		if err := newOperationSession(runner.plan, compiledPlacement(runner.plan), runner.options.Stdout, runner.options.Stderr).execute(ctx, node); err != nil {
			return err
		}
	}
	return runner.complete(ctx, runner.outputs(selected.ID))
}

func (runner *tektonRuntime) outputs(stage string) map[string]string {
	if stage == tektonSmoke {
		return map[string]string{"smoke": ".nvkr/tekton/smoke"}
	}
	if stage == tektonQualify {
		return map[string]string{"qualification": ".nvkr/tekton/qualification"}
	}
	if stage == tektonFinalize {
		return map[string]string{"result": ".nvkr/tekton/result"}
	}
	_, artifacts := planIndex(runner.plan)
	outputs := map[string]string{}
	for _, node := range runner.plan.Graph.Nodes {
		if runner.projection.owners[node.ID] != stage {
			continue
		}
		for _, id := range githubCarrierOutputs(runner.plan, node, artifacts, runner.projection.owners) {
			outputs[id] = artifacts[id].Path
		}
	}
	return outputs
}

func (runner *tektonRuntime) restore(ctx context.Context, stage string, ids []string) error {
	receipt, err := runner.authenticate(ctx, stage)
	if err != nil {
		return err
	}
	directory := filepath.Join("/nvkr/inputs", stage)
	raw, err := readTektonControl(ctx, filepath.Join(directory, "manifest.json"), tekton.MaxManifestBytes)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading Tekton carrier manifest")
	}
	set, err := tekton.AuthenticateCarriers(ctx, receipt, raw, runner.outputs(stage))
	if err != nil {
		return err
	}
	return set.Restore(ctx, directory, "/work", ids)
}

func (runner *tektonRuntime) complete(ctx context.Context, outputs map[string]string) error {
	digest, err := runner.plan.IdentityDigest()
	if err != nil {
		return err
	}
	manifest := tekton.CarrierManifest{SchemaVersion: tekton.CarrierSchema, PlanDigest: digest, Carriers: []tekton.Carrier{}}
	for index, id := range sortedKeys(outputs) {
		archive := filepath.Join("/work", ".nvkr-transfer", tekton.CarrierFile(index))
		if err := transportarchive.Pack(ctx, archive, "/work", []string{outputs[id]}); err != nil {
			return err
		}
		file, info, err := securepath.OpenRegular(archive)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "opening prepared Tekton carrier")
		}
		output, err := os.OpenFile(filepath.Join("/nvkr/output", tekton.CarrierFile(index)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			_ = file.Close()
			return e.Wrap(e.CodeImmutableViolation, err, "creating fresh Tekton carrier")
		}
		hash := sha256.New()
		_, copyErr := transportarchive.CopyExact(ctx, io.MultiWriter(output, hash), file, info.Size())
		closeSource := file.Close()
		syncErr := output.Sync()
		closeOutput := output.Close()
		for _, err := range []error{copyErr, closeSource, syncErr, closeOutput} {
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "publishing exact Tekton carrier")
			}
		}
		manifest.Carriers = append(manifest.Carriers, tekton.Carrier{ID: id, Path: outputs[id], Digest: fmt.Sprintf("sha256:%x", hash.Sum(nil)), Size: info.Size()})
	}
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, "/nvkr/output/manifest.json", raw, 0600); err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, runner.options.CarrierDigest, []byte(pipeline.FileDigest(raw)), 0600); err != nil {
		return err
	}
	return writeFactoryFile(ctx, runner.options.Outcome, []byte("success"), 0600)
}
