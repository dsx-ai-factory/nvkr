// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func nativeControllerFixtureFiles(name string) [4]string {
	return [4]string{
		fmt.Sprintf("chart:\n  name: %s\n  path: ../../controller/deploy/charts/controller\n  artifact: controller-chart\n  imageArtifact: cwe\n  profilesArtifact: controller-profiles\nrelease:\n  name: %s\n  namespace: %s\nvalues: [values.yaml, overlay.yaml]\n", name, name, name),
		"namespaceOverride: " + name + "\n",
		fmt.Sprintf("nameOverride: %s\nfullnameOverride: %s\nnamespaceOverride: %s\n", name, name, name),
		"name: nvke-controller\n",
	}
}

func nativeControllerFixture(t *testing.T, installer, name string) nativeControllerIdentity {
	t.Helper()
	for index, raw := range nativeControllerFixtureFiles(name) {
		file := filepath.Join(installer, nativeControllerSourceFiles[index])
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		writeTenantFixture(t, file, []byte(raw), 0600)
	}
	identity, err := selectNativeControllerIdentity(t.Context(), installer)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestNativeControllerIdentitySourceAdmission(t *testing.T) {
	for _, name := range []string{"nke-controller", "nvke-controller"} {
		for _, scenario := range []string{"selected", "source-owned duplicate env", "unknown", "mixed namespace", "mixed fullname", "mixed name", "null override", "base null override", "ambiguous values", "mismatched chart", "moved chart", "renamed chart artifact", "renamed image artifact", "renamed profiles artifact", "missing declaration", "duplicate declaration", "multiple value documents", "multiple release documents", "alias override", "merge override", "changed source", "symlink declaration", "canceled"} {
			t.Run(name+"/"+scenario, func(t *testing.T) {
				root := privateTestDir(t)
				selected := nativeControllerFixture(t, root, name)
				files := nativeControllerFixtureFiles(name)
				switch scenario {
				case "source-owned duplicate env":
					files[2] += "env: []\nenv: [{name: SOURCE_OWNED, value: actual}]\n"
				case "unknown":
					files[0] = strings.ReplaceAll(files[0], name, "other-controller")
				case "mixed namespace":
					files[0] = strings.Replace(files[0], "namespace: "+name, "namespace: other", 1)
				case "mixed fullname":
					files[2] = strings.Replace(files[2], "fullnameOverride: "+name, "fullnameOverride: other", 1)
				case "mixed name":
					files[2] = strings.Replace(files[2], "nameOverride: "+name, "nameOverride: other", 1)
				case "null override":
					files[2] = strings.Replace(files[2], "nameOverride: "+name, "nameOverride: null", 1)
				case "base null override":
					files[1] += "nameOverride: null\n"
				case "ambiguous values":
					files[0] = strings.Replace(files[0], "values.yaml, overlay.yaml", "overlay.yaml, values.yaml", 1)
				case "mismatched chart":
					files[3] = "name: other-controller\n"
				case "moved chart":
					files[0] = strings.Replace(files[0], "path: ../../controller/deploy/charts/controller", "path: ../../other/chart", 1)
				case "renamed chart artifact":
					files[0] = strings.Replace(files[0], "artifact: controller-chart", "artifact: other-chart", 1)
				case "renamed image artifact":
					files[0] = strings.Replace(files[0], "imageArtifact: cwe", "imageArtifact: other-image", 1)
				case "renamed profiles artifact":
					files[0] = strings.Replace(files[0], "profilesArtifact: controller-profiles", "profilesArtifact: other-profiles", 1)
				case "missing declaration":
					files[2] = "namespaceOverride: " + name + "\n"
				case "duplicate declaration":
					files[2] += "nameOverride: " + name + "\n"
				case "multiple value documents":
					files[2] += "---\nnameOverride: other\n"
				case "multiple release documents":
					files[0] += "---\nrelease: {name: other}\n"
				case "alias override":
					files[2] = "source: &name " + name + "\nnameOverride: *name\nfullnameOverride: " + name + "\nnamespaceOverride: " + name + "\n"
				case "merge override":
					files[2] += "<<: {nameOverride: other}\n"
				case "changed source":
					files[2] += "# changed exact source bytes\n"
				}
				for index, raw := range files {
					writeTenantFixture(t, filepath.Join(root, nativeControllerSourceFiles[index]), []byte(raw), 0600)
				}
				if scenario == "source-owned duplicate env" {
					var err error
					selected, err = selectNativeControllerIdentity(t.Context(), root)
					if err != nil {
						t.Fatal("unrelated values must stay source-owned", err)
					}
				}
				if scenario == "symlink declaration" {
					file := filepath.Join(root, nativeControllerSourceFiles[2])
					if err := os.Rename(file, file+".held"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(file+".held", file); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if scenario == "canceled" {
					cancel()
				}
				var got nativeControllerIdentity
				var err error
				if scenario == "changed source" {
					got, err = nativeControllerForAssets(ctx, NativeAssets{Installer: root, installerSelection: &nativeInstallerSelection{controller: selected}})
				} else {
					got, err = selectNativeControllerIdentity(ctx, root)
				}
				if scenario == "selected" || scenario == "source-owned duplicate env" {
					if err != nil || got != selected {
						t.Fatalf("source selection=%+v err=%v", got, err)
					}
				} else {
					assertNightlyCode(t, err, e.CodeEngineCapability)
					if got != (nativeControllerIdentity{}) {
						t.Fatalf("unsupported source returned identity: %+v", got)
					}
				}
			})
		}
	}
}

func TestNativeControllerIdentityRejectedBeforeConsumersWork(t *testing.T) {
	for _, name := range []string{"", "unknown-controller"} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(privateTestDir(t), "absent")
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			_, args, err := prepareSiteHelm(ctx, Runner{}, "/unavailable", out, nativeControllerIdentity{name: name})
			assertNightlyCode(t, err, e.CodeEngineCapability)
			if args != nil {
				t.Fatalf("unsupported renderer selection admitted: %v %v", args, err)
			}
			attempt := nativeSiteAttempt{assets: NativeAssets{Installer: "/must-not-be-read", installerSelection: &nativeInstallerSelection{controller: nativeControllerIdentity{name: name}}}}
			assertNightlyCode(t, attempt.admitAPIClient(ctx, Runner{}, "/unavailable", out, "/unused"), e.CodeEngineCapability)
			if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsupported identity wrote output", err)
			}
		})
	}
}

func TestNativeControllerPolicyUsesSourceSelectedIdentity(t *testing.T) {
	for _, name := range []string{"nke-controller", "nvke-controller"} {
		for _, scenario := range []string{"selected", "wrong deployment", "wrong namespace", "wrong instance", "wrong name", "wrong container", "duplicate deployment"} {
			t.Run(name+"/"+scenario, func(t *testing.T) {
				root := privateTestDir(t)
				identity := nativeControllerFixture(t, root, name)
				selector := map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/instance": name, "app.kubernetes.io/name": name}, "matchExpressions": []any{map[string]any{"key": "source-owned", "operator": "Exists"}}}
				metadata := map[string]any{"name": name, "namespace": name}
				container := map[string]any{"name": name}
				switch scenario {
				case "wrong deployment":
					metadata["name"] = "foreign"
				case "wrong namespace":
					metadata["namespace"] = "foreign"
				case "wrong instance":
					selector["matchLabels"].(map[string]any)["app.kubernetes.io/instance"] = "foreign"
				case "wrong name":
					selector["matchLabels"].(map[string]any)["app.kubernetes.io/name"] = "foreign"
				case "wrong container":
					container["name"] = "foreign"
				}
				deployment := map[string]any{"metadata": metadata, "spec": map[string]any{"selector": selector, "template": map[string]any{"spec": map[string]any{"containers": []any{container}}}}}
				items := []any{deployment}
				if scenario == "duplicate deployment" {
					items = append(items, deployment)
				}
				if err := privateJSON(filepath.Join(root, "observed.json"), map[string]any{"items": items}); err != nil {
					t.Fatal(err)
				}
				writeTenantFixture(t, filepath.Join(root, "kubectl"), []byte(`#!/bin/sh
set -eu
case "$*" in
 *"get deployments"*)
  test "$*" = "--kubeconfig unused -n $CONTROLLER get deployments -l app.kubernetes.io/instance=$CONTROLLER -o json"
  cat "$FIXTURE/observed.json";;
 *"apply --server-side"*) touch "$FIXTURE/applied";;
 *) exit 77;;
esac
`), 0700)
				attempt := nativeSiteAttempt{bridge: linuxNativeBridge(), assets: NativeAssets{Installer: root, installerSelection: &nativeInstallerSelection{controller: identity}}}
				runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root, "CONTROLLER=" + name}}
				err := attempt.admitAPIClient(t.Context(), runner, root, root, "unused")
				if scenario != "selected" {
					assertNightlyCode(t, err, e.CodeEngineCapability)
					if _, err := os.Lstat(filepath.Join(root, "nvke-api-client.json")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("mismatch wrote policy", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(root, "nvke-api-client.json"))
				if err != nil {
					t.Fatal(err)
				}
				var policy struct {
					Metadata struct{ Namespace string }
					Spec     struct {
						PodSelector map[string]any `json:"podSelector"`
					}
				}
				if err := json.Unmarshal(raw, &policy); err != nil {
					t.Fatal(err)
				}
				if policy.Metadata.Namespace != name || !reflect.DeepEqual(policy.Spec.PodSelector, selector) {
					t.Fatal("policy lost source-selected namespace or full server selector")
				}
				if _, err := os.Stat(filepath.Join(root, "applied")); err != nil {
					t.Fatal("policy not applied", err)
				}
			})
		}
	}
}
