// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command verify checks the hello demo's local record and SBOM subject.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if err := verify(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func verify() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: verify ARTIFACT")
	}
	var run struct {
		Status   string `json:"status"`
		Lane     string `json:"lane"`
		Observed []struct {
			Name string `json:"name"`
		} `json:"observed"`
	}
	if err := readJSON(".nvkr/runs/demo/lifecycle.json", &run); err != nil {
		return err
	}
	if run.Status != "complete" || run.Lane != "unqualified-local-execution" || len(run.Observed) != 1 || run.Observed[0].Name != "hello" {
		return fmt.Errorf("expected a complete unqualified local Run containing exactly hello")
	}
	artifact, err := os.ReadFile(os.Args[1])
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(artifact))
	var index struct {
		Entries []struct {
			ArtifactSHA256 string `json:"artifactSha256"`
		} `json:"entries"`
	}
	if err := readJSON(".nvkr/runs/demo/hello/sbom/index.json", &index); err != nil {
		return err
	}
	if len(index.Entries) != 1 || index.Entries[0].ArtifactSHA256 != digest {
		return fmt.Errorf("artifact/SBOM subject mismatch")
	}
	fmt.Println("  Artifact bytes match the recorded SBOM subject.")
	fmt.Println("  Artifact digest: " + digest)
	return nil
}
