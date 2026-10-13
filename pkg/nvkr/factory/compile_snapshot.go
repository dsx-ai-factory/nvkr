// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path"
	"runtime"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const InputSnapshotSchema = "nvkr.dev/compile-inputs/v1"

// InputSnapshot transports only the authored files and directory facts actually
// read by compilation. It is untrusted descriptive data, not a Plan or receipt.
// Catalog-owned authentication of upstream pinned Git trees is deliberately not
// captured here: replay retains those bounded external checks.
type InputSnapshot struct {
	SchemaVersion   string           `json:"schemaVersion"`
	DefinitionPath  string           `json:"definitionPath"`
	EnvironmentPath string           `json:"environmentPath"`
	Files           []InputFile      `json:"files"`
	Directories     []InputDirectory `json:"directories"`
}

type InputFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

// InputDirectory records an exact recursive listing, including an empty or
// absent directory. Entries are relative to Path, as in catalog.DataProvider.
type InputDirectory struct {
	Path    string   `json:"path"`
	Entries []string `json:"entries"`
}

// CaptureCompileFiles compiles repository-relative authored paths beneath root,
// without following input symlinks, and returns their complete bounded snapshot.
// Logical paths remain unchanged in the Plan regardless of root's location.
// Upstream pin-set authentication retains catalog's bounded network access.
func CaptureCompileFiles(ctx context.Context, root, definitionPath, environmentPath, generatorVersion string) (*Plan, InputSnapshot, error) {
	if err := snapshotContext(ctx); err != nil {
		return nil, InputSnapshot{}, err
	}
	if root == "" {
		return nil, InputSnapshot{}, e.New(e.CodeUsage, "input capture requires a repository root")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, InputSnapshot{}, e.New(e.CodeEngineCapability, "input capture requires bounded no-follow filesystem support")
	}
	state := newInputState()
	state.source = catalog.Dir(root).(catalog.ContextDataProvider)
	plan, err := compileInputState(ctx, state, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return nil, InputSnapshot{}, err
	}
	snapshot := state.snapshot(definitionPath, environmentPath)
	if err := validateInputSnapshot(snapshot); err != nil {
		return nil, InputSnapshot{}, err
	}
	return plan, snapshot, nil
}

// CompileSnapshot recompiles saved authored bytes with no host-file fallback.
// It does not provide offline compilation: catalog pin sets still authenticate
// their exact upstream Git trees through catalog's existing context-bound owner.
// The caller must compare the resulting Plan identity with its expected Plan.
func CompileSnapshot(ctx context.Context, snapshot InputSnapshot, generatorVersion string) (*Plan, error) {
	if err := snapshotContext(ctx); err != nil {
		return nil, err
	}
	if err := validateInputSnapshot(snapshot); err != nil {
		return nil, err
	}
	state := newInputState()
	for _, file := range snapshot.Files {
		state.files[file.Path] = append([]byte(nil), file.Data...)
	}
	for _, directory := range snapshot.Directories {
		state.directories[directory.Path] = append([]string(nil), directory.Entries...)
	}
	plan, err := compileInputState(ctx, state, snapshot.DefinitionPath, snapshot.EnvironmentPath, generatorVersion)
	if err != nil {
		return nil, err
	}
	if len(state.usedFiles) != len(state.files) || len(state.usedDirectories) != len(state.directories) {
		return nil, e.New(e.CodeInvalidCatalog, "input snapshot contains files or directory facts outside the compiled closure")
	}
	return plan, nil
}

func compileInputState(ctx context.Context, state *inputState, definitionPath, environmentPath, generatorVersion string) (*Plan, error) {
	for _, name := range []string{definitionPath, environmentPath} {
		if err := validateInputPath(name); err != nil {
			return nil, err
		}
	}
	rawDefinition, err := state.readFile(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	definition, err := authored.DecodeDefinition(rawDefinition)
	if err != nil {
		return nil, err
	}
	rawEnvironment, err := state.readFile(ctx, environmentPath)
	if err != nil {
		return nil, err
	}
	environment, err := authored.DecodeEnvironment(rawEnvironment)
	if err != nil {
		return nil, err
	}
	return compileDocumentsWithLoader(ctx, definitionPath, definition, environmentPath, environment, generatorVersion,
		func(ctx context.Context, spec authored.Catalog) (*catalog.Stack, error) {
			return state.loadStack(ctx, definitionPath, spec)
		})
}

func (state *inputState) loadStack(ctx context.Context, definitionPath string, spec authored.Catalog) (*catalog.Stack, error) {
	base, err := resolveInputPath(definitionPath, spec.Base, true)
	if err != nil {
		return nil, err
	}
	provider := &snapshotCatalogProvider{state: state, prefix: base}
	loaded, err := catalog.LoadContext(ctx, provider)
	if err != nil {
		return nil, err
	}
	overlays := make([]catalog.Overlay, 0, len(spec.Overlays))
	for _, value := range spec.Overlays {
		name, err := resolveInputPath(definitionPath, value, false)
		if err != nil {
			return nil, err
		}
		raw, err := state.readFile(ctx, name)
		if err != nil {
			return nil, err
		}
		overlay, err := catalog.DecodeOverlay(raw)
		if err != nil {
			return nil, err
		}
		overlays = append(overlays, overlay)
	}
	return catalog.ApplyStack(loaded, overlays...)
}

// Authored catalog references can traverse to a parent inside the captured root;
// the resulting transport path must always be a canonical confined relative path.
func resolveInputPath(definitionPath, value string, directory bool) (string, error) {
	if value == "" || path.IsAbs(value) || strings.ContainsAny(value, "\\:\x00") {
		return "", e.New(e.CodeInvalidCatalog, "input reference %q is not repository-relative", value)
	}
	name := path.Join(path.Dir(definitionPath), value)
	if directory && name == "." {
		return name, nil
	}
	if err := validateInputPath(name); err != nil {
		return "", err
	}
	return name, nil
}

func snapshotContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "input snapshot requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "input snapshot canceled")
	}
	return nil
}

// The catalog parser uses only the context-aware methods through BindContext.
// Refuse accidental calls to the synchronous compatibility interface.
type snapshotCatalogProvider struct {
	state  *inputState
	prefix string
}

func (provider *snapshotCatalogProvider) ReadFile(string) ([]byte, error) {
	return nil, e.New(e.CodeUsage, "snapshot catalog reads require context")
}

func (provider *snapshotCatalogProvider) ReadDir(string) ([]string, error) {
	return nil, e.New(e.CodeUsage, "snapshot catalog listings require context")
}

func (provider *snapshotCatalogProvider) Name() string { return "snapshot:" + provider.prefix }

func (provider *snapshotCatalogProvider) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	if err := validateInputPath(name); err != nil {
		return nil, err
	}
	return provider.state.readFile(ctx, path.Join(provider.prefix, name))
}

func (provider *snapshotCatalogProvider) ReadDirContext(ctx context.Context, name string) ([]string, error) {
	if err := validateInputPath(name); err != nil {
		return nil, err
	}
	return provider.state.readDirectory(ctx, path.Join(provider.prefix, name))
}
