// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/urfave/cli/v2"
)

func releaseBrowseCmd() *cli.Command {
	return &cli.Command{
		Name: "browse", Usage: "inspect exact OCI release metadata and evidence (payloads and signatures not verified)",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "reference", Required: true, Usage: "publication root repository@sha256:digest"},
			&cli.StringFlag{Name: "layout", Usage: "inspect a local OCI layout instead of pulling the registry"},
			&cli.StringFlag{Name: "format", Value: "text", Usage: "text, json or mermaid"},
			&cli.StringFlag{Name: "output", Usage: "focus on one exact named output and its evidence"},
			&cli.StringFlag{Name: "workspace", Usage: "temporary registry inspection workspace"},
			&cli.DurationFlag{Name: "timeout", Value: 10 * time.Minute, Usage: "bound the complete inspection"},
			&cli.BoolFlag{Name: "anonymous", Usage: "disable ambient registry credentials"},
			&cli.BoolFlag{Name: "plain-http", Usage: "use HTTP for a local registry"},
		}, commandPresentationFlags()...),
		Action: func(c *cli.Context) (returnedErr error) {
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			if c.NArg() != 0 || c.Duration("timeout") <= 0 {
				return e.New(e.CodeUsage, "release browse takes flags only and requires a positive timeout")
			}
			presentation := releaseview.Options{Format: c.String("format"), Output: c.String("output")}
			if err := presentation.Validate(); err != nil {
				return err
			}
			shown := false
			defer func() {
				if returnedErr != nil && view.tui && !shown {
					returnedErr = showCommandResult(c, view, releaseFailureDisplay("Release metadata browse"), returnedErr)
				}
			}()
			description, err := lifecycle.InspectRelease(c.Context, c.String("reference"), lifecycle.InspectionOptions{
				LayoutPath: c.String("layout"), WorkspaceRoot: c.String("workspace"), Timeout: c.Duration("timeout"),
				Registry: delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
			})
			if err != nil {
				return err
			}
			if view.tui {
				display, err := releasePublicationDisplay(description, presentation.Output)
				if err != nil {
					return err
				}
				shown = true
				return showCommandResult(c, view, display, nil)
			}
			raw, err := releaseview.Render(description, presentation)
			if err != nil {
				return err
			}
			if _, err := c.App.Writer.Write(raw); err != nil {
				return e.Wrap(e.CodeUnavailable, err, "writing release view")
			}
			return nil
		},
	}
}

func releaseFailureDisplay(title string) commandDisplay {
	return commandDisplay{Title: title, Note: "The requested operation did not complete. No verification or runtime health result is established."}
}

func releaseArtifactsDisplay(title, note string, artifacts []lifecycle.Artifact) commandDisplay {
	display := commandDisplay{Title: title, Note: note,
		Fields: []progress.Field{{Label: "Outputs", Value: fmt.Sprint(len(artifacts))}}}
	for _, artifact := range artifacts {
		item := progress.Item{ID: "output/" + artifact.Name, Name: artifact.Name, Kind: artifact.Kind, State: "metadata", Primary: true,
			Fields: []progress.Field{{Label: "Digest", Value: artifact.Digest}, {Label: "Build identity", Value: artifact.BuildDigest}}}
		if artifact.Disk != nil {
			item.Fields = append(item.Fields, progress.Field{Label: "Declared disk evidence", Value: declaredDiskEvidenceMode(artifact.Disk.EvidenceMode)})
		}
		if artifact.ImageIndex != nil {
			for _, platform := range artifact.ImageIndex.RequiredPlatforms {
				item.Fields = append(item.Fields, progress.Field{Label: "Required platform", Value: platform})
			}
		}
		display.Items = append(display.Items, item)
	}
	return display
}

// Delivery defines an omitted disk evidence mode as the inspected contract.
// This label describes that declaration; it neither changes signed metadata nor
// establishes that this presentation performed inspection or verification.
func declaredDiskEvidenceMode(mode string) string {
	if mode == "" {
		return "inspected"
	}
	return mode
}

func authenticatedReleaseDisplay(description lifecycle.AuthenticityDescription) commandDisplay {
	display := releaseArtifactsDisplay("Release authenticity result", "Signatures and evidence authenticated; payload integrity was not checked. Runtime health and deployment were not evaluated.", description.Artifacts)
	display.Fields = append(display.Fields,
		progress.Field{Label: "Accepted lane", Value: description.Lane},
		progress.Field{Label: "Accepted scope", Value: description.Scope},
		progress.Field{Label: "Content identity", Value: description.ContentSubject},
		progress.Field{Label: "Policy identity", Value: description.PolicyDigest},
		progress.Field{Label: "Payload integrity", Value: "not checked"})
	for index := range display.Items {
		display.Items[index].State = "authenticated"
	}
	return display
}

func verifiedReleaseDisplay(result releaseVerificationResult) commandDisplay {
	display := releaseArtifactsDisplay("Release verification result", "Verified consumer scope only; runtime health and deployment were not evaluated.", result.Release.Artifacts)
	display.Fields = append(display.Fields,
		progress.Field{Label: "Accepted lane", Value: result.Release.Lane},
		progress.Field{Label: "Accepted scope", Value: result.Release.Scope},
		progress.Field{Label: "Content identity", Value: result.Release.ContentSubject},
		progress.Field{Label: "Policy identity", Value: result.Release.PolicyDigest})
	for index := range display.Items {
		display.Items[index].State = "verified"
	}
	if exact := result.ExactInputs; exact != nil {
		display.Items = append(display.Items, progress.Item{ID: "exact-inputs", Name: "Exact authored inputs", Kind: "input binding", State: "verified",
			Fields: []progress.Field{{Label: "Definition identity", Value: exact.DefinitionDigest}, {Label: "Environment identity", Value: exact.EnvironmentDigest}, {Label: "Plan identity", Value: exact.PlanDigest}, {Label: "Content identity", Value: exact.ContentSubject}, {Label: "Policy identity", Value: exact.PolicyDigest}}})
	}
	return display
}

func releasePublicationDisplay(description delivery.PublicationDescription, selection string) (commandDisplay, error) {
	described, err := releaseview.Describe(description, selection)
	if err != nil {
		return commandDisplay{}, err
	}
	display := releaseDescriptionDisplay(described)
	display.Title = "Release metadata browse"
	return display, nil
}

// Indices stay valid as evidence items are appended; pointers into Items would
// refer to an old backing array after growth. Only visible outputs are indexed.
func releaseOutputIndices(items []progress.Item) map[string]int {
	outputs := make(map[string]int, len(items))
	for index, item := range items {
		outputs[item.ID] = index
	}
	return outputs
}

// Navigation follows the owner's exact output binding in both directions. Only
// displayed targets are linked, including when browsing one selected output.
func linkReleaseEvidence(display *commandDisplay, outputs map[string]int, evidence *progress.Item, output string) {
	if index, visible := outputs["output/"+output]; visible {
		display.Items[index].Dependencies = append(display.Items[index].Dependencies, evidence.ID)
		evidence.Dependencies = append(evidence.Dependencies, display.Items[index].ID)
	}
}

// Both owning description types project the same closed set of public evidence
// fields. Transport references, source locations and other raw values stay out.
func releaseEvidenceItem(name, kind, manifestDigest, blobDigest string, binding delivery.EvidenceBinding) progress.Item {
	item := progress.Item{ID: "evidence/" + name, Name: name, Kind: kind, State: "metadata",
		Summary: kind,
		Fields: commandFields("Stage", binding.Stage, "Component", binding.Component, "Subject kind", binding.SubjectKind, "Subject digest", binding.SubjectDigest, "Manifest digest", manifestDigest, "Blob digest", blobDigest,
			"Document digest", binding.DocumentDigest, "Inspection input digest", binding.InspectionInputDigest)}
	if binding.Platform != "" {
		item.Summary += " · " + binding.Platform
		item.Fields = append(item.Fields, progress.Field{Label: "Recorded platform", Value: binding.Platform})
	}
	// Shared evidence retains its complete membership even in a filtered view;
	// only visible members are navigable, through linkReleaseEvidence.
	for _, output := range binding.Outputs {
		item.Fields = append(item.Fields, progress.Field{Label: "Bound output", Value: output})
	}
	return item
}

func releaseDescriptionDisplay(description releaseview.Description) commandDisplay {
	display := commandDisplay{Title: "Release description export", Note: "Metadata inspection only; payloads, signatures and policy were not verified.",
		Fields: []progress.Field{
			{Label: "Outputs", Value: fmt.Sprint(len(description.Outputs))},
			{Label: "Total outputs", Value: fmt.Sprint(description.TotalOutputs)},
			{Label: "Evidence records", Value: fmt.Sprint(len(description.Evidence))},
			{Label: "Declared qualification", Value: description.DeclaredQualification},
			{Label: "Publication identity", Value: description.RootDigest},
			{Label: "Definition identity", Value: description.Metadata.DefinitionDigest},
			{Label: "Environment identity", Value: description.Metadata.EnvironmentDigest},
			{Label: "Plan identity", Value: description.Metadata.PlanDigest},
			{Label: "Content identity", Value: description.Metadata.ContentSubject},
		}}
	if description.SelectedOutput != "" {
		display.Fields = append(display.Fields, progress.Field{Label: "Selected output", Value: description.SelectedOutput})
	}
	for _, output := range description.Outputs {
		item := progress.Item{ID: "output/" + output.Name, Name: output.Name, Kind: output.Kind, State: "metadata", Primary: true,
			Summary: output.Kind,
			Fields:  []progress.Field{{Label: "Digest", Value: output.Digest}, {Label: "Build identity", Value: output.BuildDigest}}}
		if output.OS != "" || output.Arch != "" {
			item.Summary += " · " + output.OS + "/" + output.Arch
			item.Fields = append(item.Fields, progress.Field{Label: "Recorded platform", Value: output.OS + "/" + output.Arch})
		}
		if output.ChartVersion != "" {
			item.Summary += " · " + output.ChartVersion
			item.Fields = append(item.Fields, progress.Field{Label: "Chart version", Value: output.ChartVersion})
		}
		for _, component := range output.Components {
			item.Fields = append(item.Fields, progress.Field{Label: "Component", Value: component})
		}
		for _, subject := range output.BuildSubjects {
			item.Fields = append(item.Fields, progress.Field{Label: subject.Component + " build subject", Value: subject.Digest})
		}
		if disk := output.Disk; disk != nil {
			item.Fields = append(item.Fields, commandFields("Declared disk evidence", declaredDiskEvidenceMode(disk.EvidenceMode),
				"Disk format", disk.Format, "Declared encryption", disk.Encryption, "Payload digest", disk.PayloadDigest,
				"Payload bytes", fmt.Sprint(disk.Size), "Producer", disk.Producer, "Disk component", disk.Component,
				"Disk Plan identity", disk.PlanDigest, "Declared producer outcome", disk.Outcome)...)
		}
		if output.ImageIndex != nil {
			item.Summary += " · " + strings.Join(output.ImageIndex.RequiredPlatforms, ", ")
			for _, platform := range output.ImageIndex.RequiredPlatforms {
				item.Fields = append(item.Fields, progress.Field{Label: "Required platform", Value: platform})
			}
			for _, child := range output.ImageIndex.Children {
				item.Fields = append(item.Fields,
					progress.Field{Label: "Child platform", Value: child.Platform},
					progress.Field{Label: child.Platform + " component", Value: child.Component},
					progress.Field{Label: child.Platform + " manifest digest", Value: child.ManifestDigest},
					progress.Field{Label: child.Platform + " build identity", Value: child.BuildDigest},
					progress.Field{Label: child.Platform + " build subject", Value: child.BuildSubject})
			}
		}
		display.Items = append(display.Items, item)
	}
	outputs := releaseOutputIndices(display.Items)
	for _, evidence := range description.Evidence {
		item := releaseEvidenceItem(evidence.Name, evidence.Kind, evidence.ManifestDigest, evidence.BlobDigest, delivery.EvidenceBinding(evidence.Binding))
		for _, output := range evidence.Binding.Outputs {
			linkReleaseEvidence(&display, outputs, &item, output)
		}
		display.Items = append(display.Items, item)
	}
	return display
}
