// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package nkedemo consumes completed local NKD outputs for a disposable NKE
// deployment. It is a downstream diagnostic, not an NKD lifecycle, Release,
// publication, or qualification API. External NKD operations use nkdctl.
package nkedemo

import (
	"io"
	"time"
)

// RunInputs names views of completed immutable attempts, not caller-supplied artifact
// digests. The consumer checks their observations and asks nkdctl to verify and
// extract the selected bytes before installation. Every view may reference the
// same combined Run; separate completed Runs remain usable for diagnostics.
type RunInputs struct {
	OS         string
	Components string
	Providers  string
	Site       string
	Tools      string
	Profiles   string
	Simulator  string
}

// Config is deployment intent. These paths and options confer no authority.
// Out must be a new private directory outside sources and accepted attempts.
// Timeout bounds tenant provisioning; cleanup has separate bounded contexts.
type Config struct {
	Network     string
	NKECheckout string
	NKDCTL      string
	// DemoExecutable is supplied by the CLI or native Go integration, never
	// authored YAML or flags. Only its fixed privileged-port helper is invoked.
	DemoExecutable string
	Runs           RunInputs
	Out            string
	Timeout        time.Duration
	Progress       io.Writer
}

// BuildConfig selects the existing local launcher, which bootstraps and invokes
// nkdctl. It does not introduce a second build engine or in-process SDK path.
type BuildConfig struct {
	Repository    string
	NKEClone      string
	AttemptPrefix string
	Jobs          int
	Progress      io.Writer
}

const (
	InstallerCommit    = "8fb2d53368823bd386b4fbd55b4ab6d3c0931345"
	SimulatorBundle    = "nicosim-tools-arm64"
	DiskOutputName     = "nke-node-image-arm64"
	OrgID              = "novxwn552ssc"
	SiteID             = "a1b2c3d4-0001-4000-8000-000000000001"
	VPCID              = "a1b2c3d4-0002-4000-8000-000000000001"
	TenantVIP          = "172.31.0.10"
	TenantProfile      = "nke-local-26-04-arm64-plain"
	TenantInstanceType = "a1b2c3d4-0005-4000-8000-000000000003"
	TenantPrefixID     = "a1b2c3d4-0004-4000-8000-000000000001"
	SeedDigest         = "e713b6e74e884bfba5a942b10eee142171565dcec2f24c6217cd9baecea03cac"
	WorkloadImage      = "docker.io/library/alpine@sha256:260479a1cfaf304c4c20da7f8405d3ce313513dcd534bb743257bdd2fe0f3e2d"
)
