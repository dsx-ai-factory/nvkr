// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"errors"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestGitHubDiskBudgetRejectsSummedAndInspectionBounds(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range []struct {
			name, wantMessage string
			strict            bool
			profileSeconds    uint64
			materialSeconds   []uint64
		}{
			{"summed producers under individual caps", `GitHub disk stage "disk" exceeds its job duration bound`, false, 600, []uint64{21600, 21600, 21600}},
			{"strict inspection pushes bounded producer sum over maximum", `GitHub disk stage "disk" inspection exceeds its job duration bound`, true, 600, []uint64{60600}},
			{"strict inspection profile exceeds owning bound", "disk evidence join requires its selected bounded inspection timeout", true, 601, []uint64{60}},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				plan := &Plan{Environment: definition.Environment{DiskEvidence: sbom.DiskBuildProvenance,
					DiskInspectionProfiles: map[string]definition.DiskInspectionProfile{platform: {TimeoutSeconds: test.profileSeconds}}}}
				if test.strict {
					plan.Environment.DiskEvidence = ""
				}
				stage := githubStage{ID: "disk", DiskOutput: "image", Nodes: []Node{{ID: "root", Kind: NodeBuild,
					Requirements:     &execution.Requirements{TimeoutSeconds: 21600},
					ExecutionBinding: &execution.Binding{Class: execution.Class{HostPlatform: execution.HostPlatform(platform), MaxTimeoutSeconds: 21600}}}}}
				for _, seconds := range test.materialSeconds {
					stage.Nodes = append(stage.Nodes, Node{ID: "material", Kind: NodeAcquire, Requirements: &execution.Requirements{TimeoutSeconds: seconds}})
				}
				for _, node := range stage.Nodes {
					if _, err := githubDiskProducerTimeoutSeconds(node); err != nil {
						t.Fatalf("individual producer must remain under its cap: %v", err)
					}
				}
				budget, minutes, err := githubDiskStageBudget(plan, stage)
				var coded *e.Error
				if budget != 0 || minutes != 0 || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || coded.Msg != test.wantMessage {
					t.Fatalf("budget=%s minutes=%d error=%v; want no projection and %s %q", budget, minutes, err, e.CodeEngineCapability, test.wantMessage)
				}
			})
		}
	}
}

func TestGitHubDiskBudgetUsesClassAndLiveCompletion(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range []struct {
			name        string
			change      func(*Plan, *githubStage)
			wantMinutes int
			wantCode    e.Code
		}{
			{"producer provenance", nil, 505, ""},
			{"strict scans each member plus acquisitions once", func(p *Plan, _ *githubStage) { p.Environment.DiskEvidence = "" }, 540, ""},
			// This is a scheduling projection only. The owning VM runtime
			// admission still requires a positive authored producer timeout.
			{"scheduling-only zero timeout uses class ceiling", func(_ *Plan, s *githubStage) { s.Nodes[0].Requirements.TimeoutSeconds = 0 }, 505, ""},
			{"shorter producer timeout", func(_ *Plan, s *githubStage) { s.Nodes[0].Requirements.TimeoutSeconds = 60 }, 146, ""},
			{"root exceeds class", func(_ *Plan, s *githubStage) { s.Nodes[0].Requirements.TimeoutSeconds++ }, 0, e.CodeEngineCapability},
			{"missing ceiling", func(_ *Plan, s *githubStage) { s.Nodes[0].ExecutionBinding.Class.MaxTimeoutSeconds = 0 }, 0, e.CodeEngineCapability},
			{"missing root", func(_ *Plan, s *githubStage) { s.Nodes[0].ExecutionBinding = nil }, 0, e.CodeEngineCapability},
			{"two roots", func(_ *Plan, s *githubStage) { s.Nodes = append(s.Nodes, s.Nodes[0]) }, 0, e.CodeEngineCapability},
			{"missing strict profile", func(p *Plan, _ *githubStage) {
				p.Environment.DiskEvidence = ""
				p.Environment.DiskInspectionProfiles = nil
			}, 0, e.CodeEngineCapability},
			{"provider exact maximum", func(_ *Plan, s *githubStage) {
				s.Nodes = s.Nodes[:1]
				s.Nodes[0].Requirements.TimeoutSeconds = (githubMaximumDiskJobTimeoutMinutes - githubDefaultStageTimeoutMinutes - githubDiskProviderOverheadMinutes - githubDiskCleanupHeadroomMinutes - githubDiskDiagnosticUploadMinutes) * 60
				s.Nodes[0].ExecutionBinding.Class.MaxTimeoutSeconds = s.Nodes[0].Requirements.TimeoutSeconds
			}, 1440, ""},
			{"provider lacks cleanup headroom", func(_ *Plan, s *githubStage) {
				s.Nodes = s.Nodes[:1]
				s.Nodes[0].Requirements.TimeoutSeconds = (githubMaximumDiskJobTimeoutMinutes-githubDefaultStageTimeoutMinutes-githubDiskProviderOverheadMinutes-githubDiskCleanupHeadroomMinutes-githubDiskDiagnosticUploadMinutes)*60 + 1
				s.Nodes[0].ExecutionBinding.Class.MaxTimeoutSeconds = s.Nodes[0].Requirements.TimeoutSeconds
			}, 0, e.CodeEngineCapability},
			// One root and its acquisition carrier each receive ten minutes of
			// inspection; both optional diagnostic uploads need separate bounds.
			{"strict provider exact maximum", func(p *Plan, s *githubStage) {
				p.Environment.DiskEvidence = ""
				s.Nodes = s.Nodes[:1]
				s.Nodes[0].Requirements.TimeoutSeconds = 1330 * 60
				s.Nodes[0].ExecutionBinding.Class.MaxTimeoutSeconds = s.Nodes[0].Requirements.TimeoutSeconds
			}, 1440, ""},
			{"strict provider lacks upload headroom", func(p *Plan, s *githubStage) {
				p.Environment.DiskEvidence = ""
				s.Nodes = s.Nodes[:1]
				s.Nodes[0].Requirements.TimeoutSeconds = 1330*60 + 1
				s.Nodes[0].ExecutionBinding.Class.MaxTimeoutSeconds = s.Nodes[0].Requirements.TimeoutSeconds
			}, 0, e.CodeEngineCapability},
			{"overflow", func(_ *Plan, s *githubStage) {
				s.Nodes[1].Requirements = &execution.Requirements{TimeoutSeconds: ^uint64(0)}
			}, 0, e.CodeEngineCapability},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				plan := &Plan{Environment: definition.Environment{DiskEvidence: sbom.DiskBuildProvenance, DiskInspectionProfiles: map[string]definition.DiskInspectionProfile{platform: {TimeoutSeconds: 600}}}}
				stage := githubStage{ID: "disk", DiskOutput: "image", Nodes: []Node{
					{ID: "root", Kind: NodeBuild, Requirements: &execution.Requirements{TimeoutSeconds: 21600}, ExecutionBinding: &execution.Binding{Class: execution.Class{HostPlatform: execution.HostPlatform(platform), MaxTimeoutSeconds: 21600}}},
					{ID: "material", Kind: NodeAcquire},
					// These are completed live, not three additional default jobs.
					{Kind: NodeVerify}, {Kind: NodeSBOM}, {Kind: NodePackage},
				}}
				if test.change != nil {
					test.change(plan, &stage)
				}
				budget, minutes, err := githubDiskStageBudget(plan, stage)
				if test.wantCode != "" {
					assertFactoryErrorCode(t, err, test.wantCode)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if minutes != test.wantMinutes {
					t.Fatalf("job budget %d, want %d", minutes, test.wantMinutes)
				}
				if budget >= time.Duration(minutes-githubDiskCleanupHeadroomMinutes)*time.Minute {
					t.Fatal("helper deadline leaves no cleanup headroom")
				}
				boundedSteps := int((budget+time.Minute-1)/time.Minute) + githubDiskProviderOverheadMinutes + githubDiskCleanupHeadroomMinutes + githubDiskDiagnosticUploadMinutes
				if !diskBuildProvenance(plan) {
					boundedSteps += githubDiskDiagnosticUploadMinutes
				}
				if boundedSteps > minutes {
					t.Fatalf("bounded preparation/run/upload steps %d exceed job timeout %d", boundedSteps, minutes)
				}
				stage.diskExecutionBudget, stage.diskJobTimeoutMinutes = budget, minutes
				if projected, err := githubStageTimeoutMinutes(stage); err != nil || projected != minutes {
					t.Fatalf("projection %d: %v", projected, err)
				}
			})
		}
	}
}
