// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func exportLocalFixtureLayouts(t *testing.T, source *localVerifiedQualification) (string, map[string]string, string) {
	t.Helper()
	root := t.TempDir()
	ordinary := ""
	if source.build.capture.ordinary != nil {
		ordinary = filepath.Join(root, "ordinary")
		if err := source.build.capture.ordinary.WriteLayout(t.Context(), ordinary); err != nil {
			t.Fatal(err)
		}
	}
	disks := map[string]string{}
	for name, owner := range source.build.capture.disks.members {
		disks[name] = filepath.Join(root, "disk-"+name)
		if err := owner.WriteLayout(t.Context(), disks[name]); err != nil {
			t.Fatal(err)
		}
	}
	return ordinary, disks, root
}

func checkLocalLayoutCaptureFixture(t *testing.T, source *localVerifiedQualification) {
	t.Helper()
	ordinary, disks, _ := exportLocalFixtureLayouts(t, source)
	files, err := source.evidenceFiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := replayLocalQualificationInputs(t.Context(), files["plan.json"], files["lifecycle.json"], files["compile-inputs.json"])
	if err != nil {
		t.Fatal(err)
	}
	var description localBuildEvidence
	if err := canonical.UnmarshalExact(files["build-evidence.json"], &description); err != nil {
		t.Fatal(err)
	}
	capture, err := captureLocalTransportLayouts(t.Context(), inputs, description, ordinary, disks, files["disk-materials.json"])
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	raw, err := prepareLocalBuildEvidence(t.Context(), inputs, capture, source.build.policy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, files["build-evidence.json"]) {
		t.Fatal("layout round-trip changed build evidence")
	}
}

func checkRealLocalLayoutConsumer(t *testing.T, source *localVerifiedQualification, binary string) {
	t.Helper()
	checkRealLocalPublication(t, source, binary)
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/local-qualification"
	options := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	for _, mode := range []string{"exact", "missing-ordinary", "extra-disk", "changed-inputs", "qualifier-signature"} {
		t.Run("layout consumer/"+mode, func(t *testing.T) {
			ordinary, disks, root := exportLocalFixtureLayouts(t, source)
			if ordinary != "" {
				ref, err := delivery.Push(t.Context(), ordinary, repository+"/ordinary", options)
				if err != nil {
					t.Fatal(err)
				}
				ordinary = filepath.Join(root, "downloaded-ordinary")
				if _, err := delivery.Pull(t.Context(), ref, ordinary, options); err != nil {
					t.Fatal(err)
				}
			}
			for name, owner := range source.build.capture.disks.members {
				ref, err := delivery.PushDiskArtifact(t.Context(), owner, repository+"/disk-"+name, root, options)
				if err != nil {
					t.Fatal(err)
				}
				view := owner.Inspection()
				disks[name] = filepath.Join(root, "downloaded-disk-"+name)
				if err := delivery.PullDiskArtifact(t.Context(), ref, disks[name], view.Platform, view.Size, options); err != nil {
					t.Fatal(err)
				}
			}
			evidenceRef, err := source.pushEvidence(t.Context(), repository+"/evidence", options)
			if err != nil {
				t.Fatal(err)
			}
			files, err := pullLocalEvidence(t.Context(), evidenceRef, options)
			if err != nil {
				t.Fatal(err)
			}
			code := e.CodeReleaseIncomplete
			switch mode {
			case "missing-ordinary":
				ordinary = ""
			case "extra-disk":
				disks["unexpected"] = filepath.Join(root, "absent")
			case "changed-inputs":
				files["compile-inputs.json"] = []byte("{}")
				code = e.CodePipelineDrift
			case "qualifier-signature":
				files["qualification.sig"] = []byte("invalid")
				code = e.CodeSignatureInvalid
			}
			consumer, err := openLocalLayoutConsumer(t.Context(), ordinary, disks, source.build.policy, binary, files)
			if mode != "exact" {
				assertCode(t, err, code)
				if consumer != nil {
					_ = consumer.Close()
					t.Fatal("failed layout verification returned consumer")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer consumer.Close()
			// No producer workspace or result records were transported. Moving the
			// imported layouts proves later checks use retained owners, not paths.
			if err := os.Rename(root, root+".moved"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Rename(root+".moved", root) })
			if err := consumer.qualified.requireLive(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
