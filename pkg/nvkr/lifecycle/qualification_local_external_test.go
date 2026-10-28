// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"sigs.k8s.io/yaml"
)

// Opt-in consumer exercise over actual published demo outputs. It receives only
// public policy, a pinned verifier and registry locators: no producer workspace,
// signing keys, simulated smoke or fabricated build payloads. TMPDIR must have
// disk-backed capacity for the complete downloaded and captured closure.
func TestExternalLocalM3Consumer(t *testing.T) {
	resultPath := os.Getenv("NVKR_TEST_M3_RESULT")
	if resultPath == "" {
		t.Skip("set NVKR_TEST_M3_RESULT, NVKR_TEST_M3_POLICY and NVKR_TEST_COSIGN_BINARY for the real published demo")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("external local verification requires Linux")
	}
	policyPath, binary := os.Getenv("NVKR_TEST_M3_POLICY"), os.Getenv("NVKR_TEST_COSIGN_BINARY")
	if policyPath == "" || binary == "" {
		t.Fatal("explicit public policy and cosign executable are required")
	}
	policy, err := LoadLocalQualificationPolicy(t.Context(), policyPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var result LocalQualificationResult
	if err := canonical.UnmarshalExact([]byte(strings.TrimSpace(string(raw))), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || result.Publication.Evidence == "" || result.Publication.Ordinary == "" {
		t.Fatal("requires a completed local publication result; locators alone confer no trust")
	}
	options := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	files, err := pullLocalEvidence(t.Context(), result.Publication.Evidence, options)
	if err != nil {
		t.Fatal(err)
	}
	var description localBuildEvidence
	if err := canonical.UnmarshalExact(files["build-evidence.json"], &description); err != nil {
		t.Fatal(err)
	}
	if len(description.Outputs) != 29 {
		t.Fatalf("expected the complete 29-output demo, got %d", len(description.Outputs))
	}
	root := t.TempDir()
	ordinary := filepath.Join(root, "ordinary")
	if _, err := delivery.Pull(t.Context(), result.Publication.Ordinary, ordinary, options); err != nil {
		t.Fatal(err)
	}
	disks := map[string]string{}
	for _, output := range description.Outputs {
		if output.Artifact.Kind != OutputDiskImage {
			continue
		}
		name := output.Artifact.Name
		if output.Artifact.Disk == nil || result.Publication.Disks[name] == "" {
			t.Fatal("publication is missing a disk")
		}
		// Names from the evidence never form filesystem paths.
		disks[name] = filepath.Join(root, "disk")
		if len(disks) != 1 {
			t.Fatal("the maintained demo must have exactly one disk")
		}
		if err := delivery.PullDiskArtifact(t.Context(), result.Publication.Disks[name], disks[name], policy.Platform, output.Artifact.Disk.Size, options); err != nil {
			t.Fatal(err)
		}
	}
	if len(disks) != 1 || len(result.Publication.Disks) != 1 {
		t.Fatal("requires the complete one-disk demo publication")
	}
	for _, mode := range []string{"exact", "builder-signature", "smoke-signature", "qualifier-signature", "missing-signature", "missing-provenance", "changed-inputs", "wrong-source-pin", "mismatched-run", "wrong-policy-plan", "missing-ordinary", "missing-disk", "extra-disk"} {
		t.Run(mode, func(t *testing.T) {
			candidate, locations, ordinaryPath := maps.Clone(files), maps.Clone(disks), ordinary
			selectedPolicy := policy
			code := e.CodeSignatureInvalid
			switch mode {
			case "builder-signature":
				candidate["build-evidence.sig"] = []byte("invalid")
			case "smoke-signature":
				candidate["tool-smoke.sig"] = []byte("invalid")
			case "qualifier-signature":
				candidate["qualification.sig"] = []byte("invalid")
			case "missing-signature":
				delete(candidate, "qualification.sig")
				code = e.CodeReleaseIncomplete
			case "missing-provenance":
				delete(candidate, "build-evidence.json")
				code = e.CodeReleaseIncomplete
			case "changed-inputs":
				candidate["compile-inputs.json"] = []byte("{}")
				code = e.CodePipelineDrift
			case "wrong-source-pin":
				candidate["compile-inputs.json"] = substituteExternalM3Pin(t, candidate["compile-inputs.json"])
				// The local toolchain overlay binds the original catalog stack;
				// substitution fails that prerequisite before Plan replay completes.
				code = e.CodeInvalidCatalog
			case "mismatched-run":
				var run Run
				if err := canonical.UnmarshalExact(candidate["lifecycle.json"], &run); err != nil {
					t.Fatal(err)
				}
				run.Attempt = "unrelated-attempt"
				candidate["lifecycle.json"], err = canonical.Marshal(run)
				if err != nil {
					t.Fatal(err)
				}
				code = e.CodeVerifyFailed
			case "wrong-policy-plan":
				selectedPolicy.PlanDigest = "sha256:" + strings.Repeat("9", 64)
				code = e.CodeTrustPolicyMissing
			case "missing-ordinary":
				ordinaryPath = ""
				code = e.CodeReleaseIncomplete
			case "missing-disk":
				clear(locations)
				code = e.CodeReleaseIncomplete
			case "extra-disk":
				locations["unexpected"] = filepath.Join(root, "absent")
				code = e.CodeReleaseIncomplete
			}
			consumer, err := openLocalLayoutConsumer(t.Context(), ordinaryPath, locations, selectedPolicy, binary, candidate)
			if mode != "exact" {
				if consumer != nil {
					_ = consumer.Close()
					t.Fatal("negative case returned a consumer")
				}
				assertCode(t, err, code)
				if mode == "wrong-source-pin" {
					assertCode(t, errors.Unwrap(err), e.CodeUnverifiedRef)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if consumer == nil {
				t.Fatal("verification returned no consumer")
			}
			if err := consumer.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Corrupt and then remove downloaded payload bytes without touching producer
	// output or registry contents. Changes stay in this test's private scratch.
	for _, mode := range []string{"altered-payload-blob", "missing-payload-blob"} {
		t.Run(mode, func(t *testing.T) {
			names := make([]string, 0, len(disks))
			for name := range disks {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, output := range description.Outputs {
				if output.Artifact.Name != names[0] {
					continue
				}
				if !canonical.IsDigest(output.Artifact.Digest) {
					t.Fatal("disk has no canonical payload digest")
				}
				path := filepath.Join(disks[names[0]], "blobs", "sha256", strings.TrimPrefix(output.Artifact.Digest, "sha256:"))
				if mode == "missing-payload-blob" {
					if err := os.Rename(path, filepath.Join(root, "saved-disk-payload")); err != nil {
						t.Fatal(err)
					}
				} else {
					file, err := os.OpenFile(path, os.O_RDWR, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := file.Close(); err != nil {
							t.Error(err)
						}
					}()
					original := make([]byte, 1)
					if _, err := file.ReadAt(original, 0); err != nil {
						t.Fatal(err)
					}
					defer func() {
						if _, err := file.WriteAt(original, 0); err != nil {
							t.Error(err)
						}
					}()
					if _, err := file.WriteAt([]byte{original[0] ^ 1}, 0); err != nil {
						t.Fatal(err)
					}
				}
			}
			consumer, err := openLocalLayoutConsumer(t.Context(), ordinary, disks, policy, binary, files)
			if consumer != nil {
				_ = consumer.Close()
				t.Fatal("missing payload returned a consumer")
			}
			if err == nil {
				t.Fatal("missing payload passed verification")
			}
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}

// Change a real captured source tree pin while retaining a well-formed input
// snapshot. This exercises compiler identity checking, not malformed-JSON refusal.
func substituteExternalM3Pin(t *testing.T, raw []byte) []byte {
	t.Helper()
	var snapshot factory.InputSnapshot
	if err := canonical.UnmarshalExact(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	changed := 0
	for i := range snapshot.Files {
		file := &snapshot.Files[i]
		if !strings.HasSuffix(file.Path, "/nicosim/pins.lock.yaml") {
			continue
		}
		var lock catalog.PinLock
		if err := yaml.UnmarshalStrict(file.Data, &lock); err != nil {
			t.Fatal(err)
		}
		pin, exists := lock.Pins["nvke-nicosim"]
		if !exists || pin.TreeHash == strings.Repeat("9", 40) {
			t.Fatal("missing expected NICoSim pin")
		}
		pin.TreeHash = strings.Repeat("9", 40)
		lock.Pins["nvke-nicosim"] = pin
		var err error
		file.Data, err = canonical.Marshal(lock)
		if err != nil {
			t.Fatal(err)
		}
		changed++
	}
	if changed != 1 {
		t.Fatalf("expected one captured NICoSim lock, found %d", changed)
	}
	modified, err := canonical.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return modified
}
