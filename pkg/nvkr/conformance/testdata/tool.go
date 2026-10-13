// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// This static executable is a local process contract fixture, not a cluster.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var mode, state string

func has(s string) bool {
	for _, a := range os.Args[1:] {
		if a == s {
			return true
		}
	}
	return false
}
func value(prefix string) string {
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return ""
}
func output(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
func readState() map[string]string {
	var m map[string]string
	raw, _ := os.ReadFile(state)
	_ = json.Unmarshal(raw, &m)
	if m == nil {
		m = map[string]string{}
	}
	return m
}
func save(m map[string]string) { raw, _ := json.Marshal(m); _ = os.WriteFile(state, raw, 0o600) }
func main() {
	config := value("--kubeconfig=")
	if config == "" {
		os.Exit(3)
	}
	if _, err := os.ReadFile(config); err != nil {
		os.Exit(4)
	}
	if has("--conformance") {
		ns := value("--namespace=")
		m := readState()
		m["clusterroles/conformance-serviceaccount:"+ns] = "role-uid"
		m["clusterrolebindings/conformance-serviceaccount-role:"+ns] = "binding-uid"
		save(m)
		if mode == "cancel" {
			time.Sleep(30 * time.Second)
		}
		if mode != "missing" && mode != "deploy-failure" {
			raw := `<testsuite name="Kubernetes e2e suite" tests="1" failures="0"><testcase classname="Kubernetes e2e suite" name="[It] required"/></testsuite>`
			if mode == "zero" {
				raw = `<testsuite name="Kubernetes e2e suite" tests="0"/>`
			}
			if mode == "failed" {
				raw = `<testsuite name="Kubernetes e2e suite" tests="1" failures="1"><testcase classname="Kubernetes e2e suite" name="[It] required"><failure message="failure"/></testcase></testsuite>`
			}
			_ = os.WriteFile(filepath.Join(value("--output-dir="), "junit_01.xml"), []byte(raw), 0o600)
		}
		if mode == "success" || mode == "zero" || mode == "failed" {
			save(map[string]string{})
		}
		if mode == "after-failure" {
			m := readState()
			m["_finished"] = "yes"
			save(m)
		}
		if mode == "failed" || mode == "deploy-failure" {
			os.Exit(1)
		}
		return
	}
	var args []string
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "--") {
			args = append(args, a)
		}
	}
	if len(args) == 0 {
		os.Exit(5)
	}
	switch args[0] {
	case "get":
		if has("--raw=/version") {
			if mode == "after-failure" && readState()["_finished"] != "" {
				os.Exit(9)
			}
			if mode == "version-mismatch" {
				output(map[string]string{"gitVersion": "v1.34.1"})
				return
			}
			output(map[string]string{"gitVersion": "v1.35.1"})
			return
		}
		if len(args) < 2 {
			os.Exit(5)
		}
		switch args[1] {
		case "nodes":
			output(map[string]any{"items": []any{map[string]any{"metadata": map[string]string{"name": "private-node", "uid": "privateuid"}, "status": map[string]any{"nodeInfo": map[string]string{"architecture": "amd64", "operatingSystem": "linux", "kernelVersion": "6.8.0", "containerRuntimeVersion": "containerd://2.1.0", "osImage": "Ubuntu 24.04.3"}}}}})
			return
		case "pods":
			output(map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": []any{map[string]string{"imageID": "docker-pullable://private-registry/cni@sha256:" + strings.Repeat("b", 64)}}}}}})
			return
		}
		if len(args) < 3 {
			os.Exit(5)
		}
		key := args[1] + "/" + args[2]
		m := readState()
		uid := m[key]
		if uid == "" {
			return
		}
		record := map[string]any{"metadata": map[string]string{"uid": uid}}
		if args[1] == "clusterrolebindings" {
			ns := strings.SplitN(args[2], ":", 2)[1]
			record["subjects"] = []any{map[string]string{"namespace": ns, "name": "conformance-serviceaccount", "kind": "ServiceAccount"}}
			record["roleRef"] = map[string]string{"name": "conformance-serviceaccount:" + ns, "kind": "ClusterRole", "apiGroup": "rbac.authorization.k8s.io"}
			if mode == "empty-binding-subjects" {
				record["subjects"] = []any{}
			}
			if mode == "wrong-binding-role" {
				record["roleRef"] = map[string]string{"name": "unrelated-role"}
			}

		}
		if mode == "replaced" && args[1] == "namespaces" && len(m) > 1 {
			record["metadata"] = map[string]string{"uid": "replacement-uid"}
		}
		output(record)
	case "create":
		if len(args) < 3 || args[1] != "namespace" {
			os.Exit(5)
		}
		m := readState()
		m["namespaces/"+args[2]] = "namespace-uid"
		save(m)
		output(map[string]any{"metadata": map[string]string{"uid": "namespace-uid"}})
	case "delete":
		_ = os.WriteFile(state+".delete", []byte("delete attempted"), 0o600)
		if mode == "cleanup-failure" {
			os.Exit(8)
		}
		var body struct {
			Preconditions struct {
				UID string `json:"uid"`
			} `json:"preconditions"`
		}
		if err := json.NewDecoder(os.Stdin).Decode(&body); err != nil {
			os.Exit(6)
		}
		path := value("--raw=")
		parts := strings.Split(path, "/")
		key := parts[len(parts)-2] + "/" + parts[len(parts)-1]
		m := readState()
		if m[key] != body.Preconditions.UID {
			os.Exit(7)
		}
		delete(m, key)
		save(m)
		output(map[string]string{"status": "Success"})
	case "exec":
		os.Exit(1)
	default:
		os.Exit(5)
	}
}
