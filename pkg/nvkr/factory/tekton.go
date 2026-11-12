// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/tekton"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	tektonResourcePath = ".tekton/nvkr-run/resources.json"
	tektonPlanPath     = ".tekton/nvkr-run/plan.json"
	tektonManifestPath = ".tekton/nvkr-run/manifest.json"
	tektonChunkBytes   = 256 << 10
	tektonPrepare      = "nvkr-prepare"
	tektonSmoke        = "nvkr-smoke"
	tektonQualify      = "nvkr-qualify"
	tektonFinalize     = "nvkr-finalize"
)

// Kubernetes objects are output descriptions, marshaled by the canonical codec.
// None of these maps acts as an executable Plan or an authenticated receipt.
type tektonObject map[string]any

// renderTekton projects the admitted Plan to a finite Kubernetes resource list.
// Build and evidence operations remain owned by the shared NVKR runner.
func renderTekton(ctx context.Context, plan *Plan) (*GeneratedBundle, error) {
	if err := executionContext(ctx); err != nil {
		return nil, err
	}
	if plan == nil || plan.Environment.Tekton == nil {
		return nil, e.New(e.CodeEngineCapability, "Tekton generation requires explicit cluster execution and trust configuration")
	}
	settings := plan.Environment.Tekton
	projection, err := projectTektonExecution(ctx, plan, settings.Platform)
	if err != nil {
		return nil, err
	}
	layout := generationLayout{workflowPath: tektonResourcePath, manifestPath: tektonManifestPath, planPath: tektonPlanPath}
	files, digest, err := renderStaticInputs(ctx, plan, layout)
	if err != nil {
		return nil, err
	}
	prefix := "nvkr-" + strings.TrimPrefix(digest, "sha256:")[:20]
	metadata := func(name string) tektonObject {
		return tektonObject{"name": name, "namespace": settings.Namespace,
			"annotations": tektonObject{tekton.PlanAnnotation: digest}}
	}
	var resources []tektonObject
	var sources []tektonObject
	for offset := 0; offset < len(files[tektonPlanPath]); offset += tektonChunkBytes {
		index := offset / tektonChunkBytes
		name := fmt.Sprintf("%s-plan-%03d", prefix, index)
		part := fmt.Sprintf("%03d", index)
		raw := files[tektonPlanPath][offset:min(offset+tektonChunkBytes, len(files[tektonPlanPath]))]
		resources = append(resources, tektonObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(name),
			"immutable": true, "binaryData": tektonObject{part: base64.StdEncoding.EncodeToString(raw)}})
		sources = append(sources, tektonObject{"configMap": tektonObject{"name": name,
			"items": []tektonObject{{"key": part, "path": part}}}})
	}
	var tasks, overrides []tektonObject
	addTask := func(id, role string, needs, inputs []string, daemon bool, operation string) {
		spec, bindings := tektonTaskSpec(plan, prefix, id, role, inputs, sources, daemon, operation)
		task := tektonObject{"name": id, "taskSpec": tektonEmbeddedTask(spec), "workspaces": bindings,
			"params": []tektonObject{{"name": "run-name", "value": "$(context.pipelineRun.name)"},
				{"name": "run-uid", "value": "$(context.pipelineRun.uid)"}}, "timeout": "6h0m0s"}
		if len(needs) != 0 {
			task["runAfter"] = needs
		}
		if id != tektonPrepare {
			// Keep the DAG schedulable after a failure. The NVKR runner evaluates
			// the shared prerequisite model before work and records skipped work.
			// The finalizer rejects every required non-success outcome.
			task["onError"] = "continue"
		}
		tasks = append(tasks, task)
		overrides = append(overrides, tektonObject{"pipelineTaskName": id, "serviceAccountName": "nvkr-" + role})
	}
	addTask(tektonPrepare, "builder", nil, nil, false, "prepare")
	_, artifacts := planIndex(plan)
	var allStages []string
	for _, stage := range projection.stages {
		if err := executionContext(ctx); err != nil {
			return nil, err
		}
		role, daemon := "builder", false
		for _, node := range stage.Nodes {
			if node.IRNode != "" && node.Kind != NodeFetch || node.Kind == NodePackage || node.Kind == NodeSBOM {
				daemon = true
			}
			if node.IRNode != "" && node.Kind == NodeFetch {
				_, instruction, err := plan.producerInstruction(node)
				if err != nil {
					return nil, err
				}
				// The shared source owner runs dependency vendoring in its
				// pinned container before issuing the source closure receipt.
				daemon = daemon || len(instruction.VendorCmd) != 0
			}
			if tektonNodeAccount(node) == "nvkr-publisher" {
				role = "publisher"
			}
		}
		var inputStages []string
		for _, input := range stage.inputs(artifacts, projection.owners) {
			inputStages = append(inputStages, projection.owners[artifacts[input].Producer])
		}
		addTask(stage.ID, role, uniqueSorted(append([]string{tektonPrepare}, projection.dependencies[stage.ID]...)),
			uniqueSorted(inputStages), daemon, "execute")
		allStages = append(allStages, stage.ID)
	}
	allStages = uniqueSorted(allStages)
	addTask(tektonSmoke, "smoke", allStages, allStages, true, "smoke")
	addTask(tektonQualify, "qualifier", []string{tektonSmoke}, append(append([]string(nil), allStages...), tektonSmoke), false, "qualify")
	finalSpec, finalBindings := tektonTaskSpec(plan, prefix, tektonFinalize, "finalizer",
		append(append([]string(nil), allStages...), tektonSmoke, tektonQualify), sources, false, "finalize")
	pipelineSpec := tektonObject{"workspaces": []tektonObject{{"name": "carriers"}}, "tasks": tasks,
		"finally": []tektonObject{{"name": tektonFinalize, "taskSpec": tektonEmbeddedTask(finalSpec), "workspaces": finalBindings,
			"params": []tektonObject{{"name": "run-name", "value": "$(context.pipelineRun.name)"},
				{"name": "run-uid", "value": "$(context.pipelineRun.uid)"}}, "timeout": "2h0m0s"}}}
	overrides = append(overrides, tektonObject{"pipelineTaskName": tektonFinalize, "serviceAccountName": "nvkr-finalizer"})
	for _, role := range []string{"builder", "finalizer", "publisher", "qualifier", "smoke"} {
		resources = append(resources,
			tektonObject{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": metadata("nvkr-" + role), "automountServiceAccountToken": false},
			tektonObject{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": metadata("nvkr-" + role),
				"roleRef":  tektonObject{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "nvkr-execution-reader"},
				"subjects": []tektonObject{{"kind": "ServiceAccount", "name": "nvkr-" + role, "namespace": settings.Namespace}}})
	}
	resources = append(resources, tektonObject{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role",
		"metadata": metadata("nvkr-execution-reader"), "rules": []tektonObject{{"apiGroups": []string{"tekton.dev"},
			"resources": []string{"pipelineruns", "taskruns"}, "verbs": []string{"get"}}, {"apiGroups": []string{""}, "resources": []string{"persistentvolumeclaims"}, "verbs": []string{"get"}}}})
	runMetadata := metadata(prefix)
	delete(runMetadata, "name")
	runMetadata["generateName"] = prefix + "-"
	// Freeze the same quantity bytes the Kubernetes PVC codec emits (1024Gi
	// becomes 1Ti), so exact storage authentication survives API serialization.
	storage := resource.NewQuantity(int64(settings.WorkspaceGiB)<<30, resource.BinarySI).String()
	runSpec := tektonObject{"pipelineSpec": pipelineSpec, "taskRunSpecs": overrides,
		"timeouts": tektonObject{"pipeline": "24h0m0s", "tasks": "22h0m0s", "finally": "2h0m0s"},
		"taskRunTemplate": tektonObject{"serviceAccountName": "nvkr-builder", "podTemplate": tektonObject{
			"automountServiceAccountToken": false,
			"nodeSelector":                 tektonObject{"kubernetes.io/os": "linux", "kubernetes.io/arch": strings.TrimPrefix(settings.Platform, "linux/")}}},
		"workspaces": []tektonObject{{"name": "carriers", "volumeClaimTemplate": tektonObject{"metadata": tektonObject{}, "status": tektonObject{}, "spec": tektonObject{
			"accessModes": []string{"ReadWriteOnce"}, "storageClassName": settings.StorageClass, "volumeMode": "Filesystem",
			"resources": tektonObject{"requests": tektonObject{"storage": storage}}}}}}}
	execution, err := canonical.Marshal(runSpec)
	if err != nil {
		return nil, err
	}
	if len(execution) > 768<<10 {
		return nil, e.New(e.CodeEngineCapability, "Tekton embedded execution exceeds its ConfigMap size bound")
	}
	resources = append(resources,
		tektonObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(prefix + "-execution"),
			"immutable": true, "data": tektonObject{"run.json": string(execution), "plan-digest": digest}},
		tektonObject{"apiVersion": "tekton.dev/v1", "kind": "PipelineRun", "metadata": runMetadata, "spec": runSpec})
	raw, err := canonical.Marshal(tektonObject{"apiVersion": "v1", "kind": "List", "items": resources})
	if err != nil {
		return nil, err
	}
	files[tektonResourcePath] = append(raw, '\n')
	return newGeneratedBundle(EngineTekton, layout, files, digest), nil
}

func tektonNodeAccount(node Node) string {
	switch node.Kind {
	case NodeSign, NodeAttestEvidence, NodeAssembleRelease, NodePublish:
		return "nvkr-publisher"
	default:
		return "nvkr-builder"
	}
}

// Tekton v1.18 serializes these fields on EmbeddedTask, but not on TaskSpec.
// Emit their exact inert defaults so API admission preserves the Run bytes.
func tektonEmbeddedTask(spec tektonObject) tektonObject {
	wrapped := tektonObject{"metadata": tektonObject{}, "spec": nil}
	for key, value := range spec {
		wrapped[key] = value
	}
	return wrapped
}

func tektonTaskSpec(plan *Plan, prefix, id, role string, inputs []string, sources []tektonObject, daemon bool, operation string) (tektonObject, []tektonObject) {
	settings := plan.Environment.Tekton
	workspaces := []tektonObject{{"name": "output", "mountPath": "/nvkr/output"}}
	output := tektonObject{"name": "output", "workspace": "carriers", "subPath": id}
	if operation == "prepare" {
		delete(output, "subPath")
	}
	bindings := []tektonObject{output}
	for i, input := range inputs {
		name := fmt.Sprintf("input-%03d", i)
		workspaces = append(workspaces, tektonObject{"name": name, "mountPath": "/nvkr/inputs/" + input, "readOnly": true})
		bindings = append(bindings, tektonObject{"name": name, "workspace": "carriers", "subPath": input})
	}
	volumes := []tektonObject{
		{"name": "work", "emptyDir": tektonObject{}},
		{"name": "plan", "projected": tektonObject{"sources": sources}},
		{"name": "execution", "configMap": tektonObject{"name": prefix + "-execution"}},
		{"name": "api-token", "projected": tektonObject{"sources": []tektonObject{
			{"serviceAccountToken": tektonObject{"path": "token", "expirationSeconds": 28800}}}}},
	}
	mounts := []tektonObject{
		{"name": "work", "mountPath": "/work"},
		{"name": "plan", "mountPath": "/nvkr/plan", "readOnly": true},
		{"name": "execution", "mountPath": "/nvkr/execution", "readOnly": true},
		{"name": "api-token", "mountPath": "/nvkr/api", "readOnly": true},
	}
	if role == "publisher" || role == "qualifier" || role == "smoke" {
		volumes = append(volumes, tektonObject{"name": "signing-key", "secret": tektonObject{"secretName": "nvkr-" + role + "-key",
			"items": []tektonObject{{"key": "private.pem", "path": "private.pem"}}, "defaultMode": 0o400}})
		mounts = append(mounts, tektonObject{"name": "signing-key", "mountPath": "/nvkr/keys", "readOnly": true})
	}
	step := tektonObject{"name": operation, "image": settings.RunnerImage, "workingDir": "/work",
		"command": []string{"/usr/local/bin/nvkr"}, "args": []string{"factory", "tekton", operation,
			"--stage", id, "--plan-parts", "/nvkr/plan", "--execution", "/nvkr/execution",
			"--run-name", "$(params.run-name)", "--run-uid", "$(params.run-uid)",
			"--task-run", "$(context.taskRun.name)", "--task-uid", "$(context.taskRun.uid)",
			"--outcome", "$(results.outcome.path)", "--carrier-digest", "$(results.carrier.path)"},
		"volumeMounts": mounts,
		"computeResources": tektonObject{"requests": tektonObject{"cpu": "1", "memory": "1Gi"},
			"limits": tektonObject{"cpu": "2", "memory": "4Gi"}},
		"securityContext": tektonObject{"allowPrivilegeEscalation": false, "capabilities": tektonObject{"drop": []string{"ALL"}}}}
	spec := tektonObject{"params": []tektonObject{{"name": "run-name", "type": "string"}, {"name": "run-uid", "type": "string"}},
		"workspaces": workspaces, "results": []tektonObject{{"name": "outcome", "type": "string"}, {"name": "carrier", "type": "string"}},
		"steps": []tektonObject{step}}
	if daemon {
		// Held runtime execution fixes this endpoint independently of ambient
		// DOCKER_HOST. The socket directory is pod-private, never a host mount.
		volumes = append(volumes, tektonObject{"name": "docker-socket", "emptyDir": tektonObject{}}, tektonObject{"name": "docker-data", "emptyDir": tektonObject{}})
		mounts = append(mounts, tektonObject{"name": "docker-socket", "mountPath": "/var/run"})
		step["volumeMounts"] = mounts
		step["env"] = []tektonObject{{"name": "DOCKER_HOST", "value": "unix:///var/run/docker.sock"}}
		spec["sidecars"] = []tektonObject{{"name": "runtime", "image": settings.RuntimeImage,
			"command": []string{"dockerd"}, "args": []string{"--host=unix:///var/run/docker.sock", "--storage-driver=vfs"},
			"securityContext": tektonObject{"privileged": true},
			"volumeMounts": []tektonObject{{"name": "work", "mountPath": "/work"}, {"name": "docker-socket", "mountPath": "/var/run"},
				{"name": "docker-data", "mountPath": "/var/lib/docker"}},
			"readinessProbe": tektonObject{"exec": tektonObject{"command": []string{"docker", "--host=unix:///var/run/docker.sock", "info"}},
				"periodSeconds": 2, "timeoutSeconds": 2, "failureThreshold": 60},
			"computeResources": tektonObject{"requests": tektonObject{"cpu": "1", "memory": "2Gi"},
				"limits": tektonObject{"cpu": "2", "memory": "4Gi"}}}}
	}
	spec["volumes"] = volumes
	return spec, bindings
}
