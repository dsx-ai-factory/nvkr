// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package planview describes validated generated Plans without granting execution,
// artifact verification, or release authority. Views never replace canonical plans.
package planview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Document is an owned snapshot of one fully validated Plan. Its private state
// prevents subsequent caller mutation from changing a rendered view.
// It is a description, never an authority receipt.
type Document struct {
	plan   factory.Plan
	raw    []byte
	digest string
}

// Load reads a bounded, no-follow canonical Plan through its owning module.
func Load(ctx context.Context, path string) (*Document, error) {
	plan, err := factory.LoadPlanContext(ctx, path)
	if err != nil {
		return nil, err
	}
	return Inspect(plan)
}

// Inspect validates every graph and binding before retaining an owned snapshot.
func Inspect(plan *factory.Plan) (*Document, error) {
	if plan == nil {
		return nil, e.New(e.CodeUsage, "plan inspection requires a plan")
	}
	digest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "serializing inspected plan")
	}
	var snapshot factory.Plan
	// These bytes were just produced by the canonical codec from the validated
	// Plan. Decode an owned typed copy without repeating the canonical round trip;
	// external bytes still enter only through factory.LoadPlanContext.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "snapshotting inspected plan")
	}
	return &Document{plan: snapshot, raw: raw, digest: digest}, nil
}

// PlanOptions selects a display format; Pretty applies only to the JSON view.
type PlanOptions struct {
	Format string
	Pretty bool
}

func (o PlanOptions) Validate() error {
	if o.Format != "text" && o.Format != "json" {
		return e.New(e.CodeUsage, "unknown plan format %q (use text or json)", o.Format)
	}
	if o.Pretty && o.Format != "json" {
		return e.New(e.CodeUsage, "--pretty requires JSON output")
	}
	return nil
}

// Render emits exact canonical bytes for JSON, or a separate human-readable view.
func (d *Document) Render(o PlanOptions) ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if err := d.valid(); err != nil {
		return nil, err
	}
	if o.Format == "json" {
		if !o.Pretty {
			return bytes.Clone(d.raw), nil
		}
		var out bytes.Buffer
		if err := json.Indent(&out, d.raw, "", "  "); err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "formatting inspected plan")
		}
		out.WriteByte('\n')
		return out.Bytes(), nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "definition: %s\ndefinitionDigest: %s\nenvironmentDigest: %s\nrun plan: %s\nexpected outputs: %d\nlane: %s\n", d.plan.Name, d.plan.DefinitionDigest, d.plan.EnvironmentDigest, d.digest, len(d.plan.LifecycleOutputs), d.plan.Qualification)
	fmt.Fprintf(&out, "Planned work only; no runtime observations or release acceptance.\n\nOutputs\n")
	for _, output := range d.plan.LifecycleOutputs {
		fmt.Fprintf(&out, "  %s (%s) -> %s\n    build: %s\n    components: %s\n", output.Name, output.Kind, output.ArtifactID, output.BuildDigest, strings.Join(output.Components, ", "))
	}
	fmt.Fprintf(&out, "\nProducer instructions\n")
	for _, build := range d.plan.Builds {
		fmt.Fprintf(&out, "  %s [%s]\n", build.Name, build.Platform)
		for _, node := range build.Graph.Nodes {
			fmt.Fprintf(&out, "    %s %s\n", node.Kind, node.ID)
			if len(node.Needs) > 0 {
				fmt.Fprintf(&out, "      requires: %s\n", strings.Join(node.Needs, ", "))
			}
			if err := renderProducerFields(&out, node); err != nil {
				return nil, err
			}
		}
	}
	fmt.Fprintf(&out, "\nArtifact operations\n")
	for _, node := range d.plan.Graph.Nodes {
		execution, err := d.plan.ExecutionFor(node)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&out, "  %s (%s) runner=%q network=%q\n", node.ID, node.Kind, execution.RunsOn, execution.Network)
		if node.IRNode != "" {
			fmt.Fprintf(&out, "    instruction: %s/%s\n", node.BuildTarget, node.IRNode)
		}
		if len(node.Inputs) > 0 {
			fmt.Fprintf(&out, "    inputs: %s\n", strings.Join(node.Inputs, ", "))
		}
		if len(node.Outputs) > 0 {
			fmt.Fprintf(&out, "    outputs: %s\n", strings.Join(node.Outputs, ", "))
		}
		if node.HelmImage != nil {
			fmt.Fprintf(&out, "    image bindings: %s\n", jsonDisplay(node.HelmImage))
		}
	}
	fmt.Fprintf(&out, "\nPublication routes\n")
	if len(d.plan.Routes) == 0 {
		out.WriteString("  none\n")
	}
	for _, route := range d.plan.Routes {
		target := d.plan.Targets[route.Target]
		fmt.Fprintf(&out, "  %s: %s -> %q (%s)\n", route.Name, route.Input, target.Ref, target.Kind)
	}
	return []byte(out.String()), nil
}

func (d *Document) valid() error {
	if d == nil || len(d.raw) == 0 {
		return e.New(e.CodeUsage, "plan view requires an inspected document")
	}
	return nil
}

// renderProducerFields uses the serialized IR schema so newly admitted producer
// fields cannot silently disappear from the text view. RawMessage preserves exact
// numeric values, and field sorting changes presentation only, never array order.
func renderProducerFields(out *strings.Builder, node ir.Node) error {
	raw, err := json.Marshal(node)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "serializing producer instructions")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return e.Wrap(e.CodeInternal, err, "reading producer instruction fields")
	}
	for _, key := range keys(fields) {
		if key == "id" || key == "kind" || key == "needs" {
			continue // The node header and dependency line already display these.
		}
		label := key
		switch key {
		case "command":
			label = "command argv"
		case "vendorCmd":
			label = "vendorCmd argv"
		}
		fmt.Fprintf(out, "      %s: %s\n", label, jsonDisplay(fields[key]))
	}
	return nil
}

func jsonDisplay(value any) string {
	// Only validated schema values and derived descriptions reach this helper.
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%q", err.Error())
	}
	// encoding/json escapes ASCII controls, but leaves Unicode format controls
	// such as bidi overrides intact. Escape every nonprinting rune for terminal
	// display while retaining valid JSON strings and their exact decoded values.
	var out strings.Builder
	for _, r := range string(raw) {
		if strconv.IsPrint(r) {
			out.WriteRune(r)
		} else if r <= 0xffff {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&out, "\\u%04x\\u%04x", hi, lo)
		}
	}
	return out.String()
}

func keys[V any](values map[string]V) []string {
	return slices.Sorted(maps.Keys(values))
}
