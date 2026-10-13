// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package nvkee2estaging prepares the downstream NVKE suite from a completed
// Run. It delegates source, publication and material integrity to their owners;
// its configuration and reports confer no release or runtime authority.
package nvkee2estaging

import (
	"io"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// Options selects exact inputs and an isolated preparation directory. PlanOnly
// performs no source inspection, subprocess invocation or registry acquisition.
type Options struct {
	Run, Plan, Definition, Environment, Inputs, Out               string
	Repository, RunID, RunAttempt, Reference, SourceNode          string
	StreamName, GPU, K8s, Arch, Ubuntu, Versions, GitHubServerURL string
	Cosign, CosignDigest, Identity, Issuer, MinimumDiskEvidence   string
	PlanOnly, CompileOnly                                         bool
	Diagnostics                                                   io.Writer
}

// Report is a descriptive preparation record, never a verification receipt.
type Report struct {
	Repository             string                            `json:"repository"`
	RunID                  string                            `json:"run_id"`
	RunAttempt             string                            `json:"run_attempt"`
	PublicationRef         string                            `json:"publication_ref"`
	InputIdentities        map[string]string                 `json:"input_identities,omitempty"`
	StreamName             string                            `json:"stream_name"`
	GPU                    string                            `json:"gpu"`
	K8s                    string                            `json:"k8s"`
	HarnessCommit          string                            `json:"harness_commit"`
	RunRecordStatus        string                            `json:"run_record_status,omitempty"`
	RunRecordTrust         string                            `json:"run_record_trust"`
	SourceTrust            string                            `json:"source_trust"`
	ValidationOutcome      string                            `json:"validation_outcome"`
	CompilationOutcome     string                            `json:"compilation_outcome"`
	Source                 factory.RestoredSourceDescription `json:"source"`
	Artifacts              map[string]any                    `json:"artifacts"`
	MaterializationPhase   string                            `json:"materialization_phase"`
	MaterializationOutcome string                            `json:"materialization_outcome"`
	TestOutcome            string                            `json:"test_outcome"`
	CleanupOutcome         string                            `json:"cleanup_outcome"`
	GoTestCommand          []string                          `json:"go_test_command,omitempty"`
	GoTestDirectory        string                            `json:"go_test_directory,omitempty"`
	RuntimeAdmission       string                            `json:"runtime_admission,omitempty"`
	LiveCommandAdmitted    bool                              `json:"live_command_admitted"`
	RemainingInputGates    []string                          `json:"remaining_input_gates,omitempty"`
	QualificationAuthority string                            `json:"qualification_authority"`
	ErrorCode              string                            `json:"error_code,omitempty"`
	ErrorClass             string                            `json:"error_class,omitempty"`
	SubprocessExitCode     *int                              `json:"subprocess_exit_code,omitempty"`
}

type fixture struct {
	Identity string `json:"identity"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
}

type binding struct {
	Output      string   `json:"output"`
	Fixture     *fixture `json:"fixture,omitempty"`
	InstallPath string   `json:"install_path,omitempty"`
	Repository  string   `json:"repository,omitempty"`
	Digest      string   `json:"digest,omitempty"`
}
