// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"io"
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// bootstrapDiagnosticReport is an upstream terminal assertion marker, not a
// duplicated inventory of cases or proof of complete external-resource cleanup.
type bootstrapDiagnosticReport struct {
	APIVersion      string `json:"apiVersion"`
	Suite           string `json:"suite"`
	Completed       bool   `json:"completed"`
	Phase           string `json:"phase"`
	ExitCode        *int   `json:"exitCode"`
	Before          string `json:"artifactSha256Before"`
	After           string `json:"artifactSha256After"`
	Cleanup         string `json:"cleanup"`
	Nodes           int    `json:"nodes"`
	ControlPlaneVIP *bool  `json:"controlPlaneVip"`
}

func readBootstrapDiagnosticReport(path, checksum string) (*bootstrapDiagnosticReport, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "opening required upstream bootstrap terminal report")
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return nil, e.New(e.CodeBuildFailed, "unreadable or oversized bootstrap report")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "invalid bootstrap report JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var report bootstrapDiagnosticReport
	if err := decoder.Decode(&report); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "decoding upstream bootstrap report")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, e.New(e.CodeBuildFailed, "trailing or oversized bootstrap report")
	}
	if report.APIVersion != "nvke.dev/bootstrap-e2e/v1" || report.Suite != "bootstrap" || !report.Completed || report.Phase != "complete" || report.ExitCode == nil || *report.ExitCode != 0 || report.Nodes < 1 || report.ControlPlaneVIP == nil || (report.Nodes > 1 && !*report.ControlPlaneVIP) {
		return nil, e.New(e.CodeBuildFailed, "missing or incomplete upstream bootstrap completion")
	}
	if report.Before != checksum || report.After != checksum {
		return nil, e.New(e.CodeVerifyFailed, "bootstrap report does not identify the unchanged staged executable")
	}
	if report.Cleanup != "unknown" {
		return nil, e.New(e.CodeBuildFailed, "upstream bootstrap cleanup failed or has an unsupported status")
	}
	return &report, nil
}
