// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type compositeRuntimeFixture struct {
	composition authored.Composition
	environment authored.Environment
	files       map[string][]byte
	load        func(context.Context, authored.Catalog) (*catalog.Stack, error)
}

func newCompositeRuntimeFixture(t *testing.T, changes ...func(*authored.Definition)) compositeRuntimeFixture {
	t.Helper()
	input, environment, base, stacks := runtimeConsumerInputsFixture(t, []string{"first", "second", "third", "fourth"})
	files := map[string][]byte{}
	composite := authored.CompositeDefinition{APIVersion: authored.CompositeAPIVersion, Kind: authored.CompositeKind, Metadata: authored.Metadata{Name: "runtime-product"}}
	for index, name := range []string{"alpha", "beta"} {
		member := input.Clone()
		member.Metadata.Name = name
		member.Outputs, member.InternalTools = nil, nil
		for i, output := range input.Outputs {
			if i%2 != index {
				continue
			}
			member.Outputs = append(member.Outputs, output)
			for _, tool := range input.InternalTools {
				if environment.Runtime.ProducedRuntimes[output.Name] == tool.Name {
					member.InternalTools = append(member.InternalTools, tool)
				}
			}
		}
		for _, change := range changes {
			if change != nil {
				change(&member)
			}
		}
		digest, err := member.Digest()
		if err != nil {
			t.Fatal(err)
		}
		files["defs/"+name+".json"] = mustCanonical(t, member)
		composite.Members = append(composite.Members, authored.CompositeMember{Name: name, Definition: name + ".json", Digest: digest})
	}
	files["defs/composite.json"] = mustCanonical(t, composite)
	composition, err := authored.AdmitComposite(t.Context(), "defs/composite.json", files["defs/composite.json"], func(_ context.Context, name string) ([]byte, error) {
		raw, ok := files[name]
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "missing fixture member %q", name)
		}
		return raw, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return compositeRuntimeFixture{composition: composition, environment: environment, files: files,
		load: func(_ context.Context, selection authored.Catalog) (*catalog.Stack, error) {
			switch selection.Base {
			case "catalog":
				return base, nil
			case "runtime-catalog":
				return stacks["internal/runtime-first"], nil
			default:
				return nil, e.New(e.CodeInvalidCatalog, "unknown fixture catalog %q", selection.Base)
			}
		}}
}

func TestCompositeRuntimeBuildIdentityAndIndependentRequirements(t *testing.T) {
	fixture := newCompositeRuntimeFixture(t)
	plan, err := compileComposition(t.Context(), fixture.composition, "defs/environment.json", fixture.environment, "test", fixture.load)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(plan.InternalTools) != 4 || len(plan.LifecycleOutputs) != 4 || len(plan.Composition.Outputs) != 4 || len(plan.Builds) != 8 {
		t.Fatal("composite changed required runtime or delivered membership")
	}
	builds := map[string]string{}
	for _, build := range plan.Builds {
		builds[build.Name] = build.BuildDigest
	}
	seen := map[string]bool{}
	for _, tool := range plan.InternalTools {
		if tool.BuildDigest != plan.InternalTools[0].BuildDigest {
			t.Fatal("equal runtime selections changed Build identity")
		}
		for _, artifact := range tool.inputs() {
			if seen[artifact] {
				t.Fatal("composite runtimes share a required artifact or evidence slot")
			}
			seen[artifact] = true
		}
	}
	for _, member := range fixture.composition.Members() {
		environment := fixture.environment.Clone()
		environment.Runtime.ProducedRuntimes = map[string]string{}
		for _, output := range member.Definition.Outputs {
			environment.Runtime.ProducedRuntimes[output.Name] = fixture.environment.Runtime.ProducedRuntimes[output.Name]
		}
		standalone, err := compileDocumentsWithLoader(t.Context(), member.Path, member.Definition, "defs/environment.json", environment, "test", fixture.load)
		if err != nil {
			t.Fatal(err)
		}
		for _, build := range standalone.Builds {
			if builds[build.Name] != build.BuildDigest {
				t.Fatalf("composition changed Build %s", build.Name)
			}
		}
	}
	// A saved Plan cannot change the selected consumer without changing its
	// authenticated Environment identity. Replay checks source membership too.
	plan.Environment.Runtime.ProducedRuntimes["first"], plan.Environment.Runtime.ProducedRuntimes["fourth"] =
		plan.Environment.Runtime.ProducedRuntimes["fourth"], plan.Environment.Runtime.ProducedRuntimes["first"]
	assertFactoryErrorCode(t, plan.Validate(), e.CodeUnverifiedRef)
}

func TestCompositeRuntimeAdmissionBeforeCatalogWork(t *testing.T) {
	for _, tc := range []struct {
		name         string
		change       func(*authored.Environment)
		memberChange func(*authored.Definition)
	}{
		{"cross-member swap", func(env *authored.Environment) {
			env.Runtime.ProducedRuntimes["first"], env.Runtime.ProducedRuntimes["fourth"] = env.Runtime.ProducedRuntimes["fourth"], env.Runtime.ProducedRuntimes["first"]
		}, nil},
		{"unbound tool", func(env *authored.Environment) { delete(env.Runtime.ProducedRuntimes, "first") }, nil},
		{"unknown tool", func(env *authored.Environment) { env.Runtime.ProducedRuntimes["first"] = "unknown" }, nil},
		{"unknown consumer", func(env *authored.Environment) {
			env.Runtime.ProducedRuntimes["unknown"] = env.Runtime.ProducedRuntimes["first"]
			delete(env.Runtime.ProducedRuntimes, "first")
		}, nil},
		{"shared tool", func(env *authored.Environment) {
			env.Runtime.ProducedRuntimes["fourth"] = env.Runtime.ProducedRuntimes["first"]
		}, nil},
		{"non-disk consumer", nil, func(member *authored.Definition) { member.Outputs[0].Format = authored.OutputOCI }},
		{"cross-platform consumer", nil, func(member *authored.Definition) { member.Outputs[0].Platform = "linux/arm64" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCompositeRuntimeFixture(t, tc.memberChange)
			if tc.change != nil {
				tc.change(&fixture.environment)
			}
			loads := 0
			plan, err := compileComposition(t.Context(), fixture.composition, "defs/environment.json", fixture.environment, "test", func(ctx context.Context, selection authored.Catalog) (*catalog.Stack, error) {
				loads++
				return fixture.load(ctx, selection)
			})
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if plan != nil || loads != 0 {
				t.Fatal("invalid member binding loaded a catalog or returned a Plan")
			}
		})
	}
}

func TestCompositeRuntimeCapturedReplayChecksDeclaringMember(t *testing.T) {
	fixture := newCompositeRuntimeFixture(t)
	fixture.environment.Runtime.ProducedRuntimes["first"], fixture.environment.Runtime.ProducedRuntimes["fourth"] =
		fixture.environment.Runtime.ProducedRuntimes["fourth"], fixture.environment.Runtime.ProducedRuntimes["first"]
	fixture.files["defs/environment.json"] = mustCanonical(t, fixture.environment)
	snapshot := InputSnapshot{SchemaVersion: InputSnapshotSchema, DefinitionPath: "defs/composite.json", EnvironmentPath: "defs/environment.json"}
	for _, name := range sortedKeys(fixture.files) {
		snapshot.Files = append(snapshot.Files, InputFile{Path: name, Data: fixture.files[name]})
	}
	// Deliberately include no catalog files: source ownership must be checked
	// before replay can attempt to read those inputs.
	plan, err := CompileSnapshot(t.Context(), snapshot, "test")
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if plan != nil {
		t.Fatal("cross-member snapshot returned a Plan")
	}
}

func TestCompositeRuntimeProducerComparisonUsesConsumerMember(t *testing.T) {
	fixture := newCompositeRuntimeFixture(t)
	plan, err := compileComposition(t.Context(), fixture.composition, "defs/environment.json", fixture.environment, "test", fixture.load)
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan.Builds {
		if plan.Builds[i].Name == "internal/runtime-first" {
			plan.Builds[i].ResolvedComponents[0].Commit = strings.Repeat("f", 40)
		}
	}
	assertFactoryErrorCode(t, checkCrossMemberProducers(*plan, plan.Composition), e.CodeInvalidCatalog)
}
