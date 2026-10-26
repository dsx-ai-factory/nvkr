// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

const demoFileLimit = 64 << 10

// Tenant readiness is separately bounded; this does not extend the E2E context.
const (
	MinTenantTimeoutSeconds     = 300
	DefaultTenantTimeoutSeconds = 3600
	MaxTenantTimeoutSeconds     = 7200
)

// DemoFile is downstream deployment intent, not an NVKR Definition or receipt.
// Loading descriptions never builds, deploys, restores, or writes configuration.
type DemoFile struct {
	Version int         `yaml:"version"`
	Build   BuildFile   `yaml:"build"`
	Run     RunFile     `yaml:"run"`
	Restore RestoreFile `yaml:"restore"`
}

type BuildFile struct {
	QualifyLocal        bool              `yaml:"qualifyLocal"`
	Prepared            bool              `yaml:"prepared"`
	Qualification       QualificationFile `yaml:"qualification"`
	QualificationPolicy string            `yaml:"qualificationPolicy"`
	Repo                string            `yaml:"repo"`
	NVKEClone           string            `yaml:"nvkeClone"`
	Definition          string            `yaml:"definition"`
	Environment         string            `yaml:"environment"`
	NVKR                string            `yaml:"nvkr"`
	Attempt             string            `yaml:"attempt"`
	Jobs                int               `yaml:"jobs"`
}

type RunFile struct {
	Resources      NativeResources `yaml:"resources"`
	TenantWorkers  int             `yaml:"tenantWorkers"`
	Accelerator    string          `yaml:"accelerator"`
	Cleanup        bool            `yaml:"cleanup"`
	Walkthrough    bool            `yaml:"walkthrough"`
	Conformance    bool            `yaml:"conformance"`
	Repo           string          `yaml:"repo"`
	Network        string          `yaml:"network"`
	NVKECheckout   string          `yaml:"nvkeCheckout"`
	NVKR           string          `yaml:"nvkr"`
	Runs           RunFileInputs   `yaml:"runs"`
	Out            string          `yaml:"out"`
	TimeoutSeconds int             `yaml:"timeoutSeconds"`
}

type RunFileInputs struct {
	Artifacts  string `yaml:"artifacts"`
	HostTools  string `yaml:"hostTools"`
	Runtime    string `yaml:"runtime"`
	OS         string `yaml:"os"`
	Components string `yaml:"components"`
	Providers  string `yaml:"providers"`
	Site       string `yaml:"site"`
	Tools      string `yaml:"tools"`
	Profiles   string `yaml:"profiles"`
	Simulator  string `yaml:"simulator"`
}

type RestoreFile struct {
	Work string `yaml:"work"`
}

// LoadDemoFile uses only the explicitly selected regular YAML file.
// There is no config search, environment binding, watcher,
// remote provider, or configuration writer. Paths stay descriptions until the
// selected operation's existing admission checks validate them.
func LoadDemoFile(ctx context.Context, path string) (DemoFile, error) {
	var result DemoFile
	if err := requireContext(ctx); err != nil {
		return result, nvkrerr.Wrap(nvkrerr.CodeUsage, err, "demo configuration context")
	}
	file, info, err := securepath.OpenRegular(path)
	if err != nil {
		return result, configUsage("require an explicit regular demo configuration file")
	}
	defer func() { _ = file.Close() }()
	if info.Size() <= 0 || info.Size() > demoFileLimit {
		return result, configUsage("demo configuration must contain at most 64 KiB")
	}
	var contents bytes.Buffer
	if _, err = transportarchive.CopyExact(ctx, &contents, file, info.Size()); err != nil {
		if ctx.Err() != nil {
			return result, nvkrerr.Wrap(nvkrerr.CodeUsage, ctx.Err(), "demo configuration context")
		}
		return result, configUsage("cannot read complete demo configuration")
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) {
		return result, configUsage("demo configuration changed during loading")
	}
	raw := contents.Bytes()
	// The existing YAML owner rejects duplicate/unknown fields and additional
	// documents. Its typed decoder alone permits float-to-int conversion, so
	// inspect its parsed tags too: this schema contains only maps, strings,
	// integers and the typed cleanup boolean. No scalar grammar is implemented.
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var described DemoFile
	if err = decoder.Decode(&described); err != nil {
		return result, configUsage("invalid demo configuration schema or types")
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return result, configUsage("require exactly one YAML configuration document")
	}
	var document yaml.Node
	if err = yaml.Unmarshal(raw, &document); err != nil || !demoScalarTypes(&document, 0) {
		return result, configUsage("demo configuration requires string keys and strict scalar types")
	}
	if described.Version != 1 || (described.Build == (BuildFile{}) && described.Run == (RunFile{}) && described.Restore == (RestoreFile{})) {
		return result, configUsage("require version 1 and a nonempty build, run or restore section")
	}
	var values map[string]any
	if err = document.Decode(&values); err != nil {
		return result, configUsage("cannot decode demo configuration")
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return result, configUsage("cannot decode demo configuration")
	}
	result = DemoFile{Build: BuildFile{Jobs: 1}, Run: RunFile{Network: "ovn", TimeoutSeconds: DefaultTenantTimeoutSeconds}}
	// JSON's typed assignment rejects YAML's implicit scalar conversions while
	// preserving defaults only for omitted fields. The YAML owner above admits
	// the exact schema, key spelling, tags and single-document boundary.
	typed := json.NewDecoder(bytes.NewReader(encoded))
	typed.DisallowUnknownFields()
	if err = typed.Decode(&result); err != nil {
		return DemoFile{}, configUsage("invalid demo configuration schema or types")
	}
	if err = requireContext(ctx); err != nil {
		return DemoFile{}, nvkrerr.Wrap(nvkrerr.CodeUsage, err, "demo configuration context")
	}
	return result, nil
}

func demoScalarTypes(node *yaml.Node, depth int) bool {
	if depth > 64 {
		return false
	}
	switch node.Kind {
	case yaml.DocumentNode:
		return len(node.Content) == 1 && node.Content[0].Kind == yaml.MappingNode && demoScalarTypes(node.Content[0], depth+1)
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !demoScalarTypes(node.Content[i+1], depth+1) {
				return false
			}
		}
		return true
	case yaml.ScalarNode:
		return node.Tag == "!!str" || node.Tag == "!!int" || node.Tag == "!!bool"
	default:
		return false
	}
}

func configUsage(message string) error { return nvkrerr.New(nvkrerr.CodeUsage, "%s", message) }

func configString(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\x00\r\n")
}

func (file DemoFile) BuildConfig(progress io.Writer) (BuildConfig, error) {
	build := file.Build
	if err := validateAutomaticQualification(build.QualifyLocal, build.Prepared, build.QualificationPolicy, build.Qualification); err != nil {
		return BuildConfig{}, err
	}
	network, err := normalizeNetwork(file.Run.Network)
	if err != nil {
		return BuildConfig{}, err
	}
	if build.Prepared && !configString(build.QualificationPolicy) {
		return BuildConfig{}, configUsage("prepared builds require an explicit qualification policy")
	}
	if err := build.Qualification.validate(build.QualificationPolicy); err != nil {
		return BuildConfig{}, err
	}
	if build.QualificationPolicy != "" && !configString(build.QualificationPolicy) {
		return BuildConfig{}, configUsage("qualificationPolicy requires an explicit policy path")
	}
	if file.Version != 1 || !configString(build.Repo) || !configString(build.Attempt) || build.Jobs < 1 || (build.NVKEClone != "" && !configString(build.NVKEClone)) {
		return BuildConfig{}, configUsage("build requires version 1, repo, attempt and positive jobs")
	}
	if err := validateSelectedBuild(build.Definition, build.Environment, build.NVKR, build.Prepared || build.QualifyLocal || build.QualificationPolicy != ""); err != nil {
		return BuildConfig{}, err
	}
	return BuildConfig{Definition: build.Definition, Environment: build.Environment, NVKR: build.NVKR, QualifyLocal: build.QualifyLocal, Repository: build.Repo, NVKEClone: build.NVKEClone, AttemptPrefix: build.Attempt, Jobs: build.Jobs, Progress: progress, QualificationPolicy: build.QualificationPolicy, Qualification: build.Qualification, Prepared: build.Prepared, Network: network, Conformance: file.Run.Conformance}, nil
}

func (file DemoFile) runDescription(progress io.Writer) (Config, error) {
	run := file.Run
	if _, err := selectedNativeTenantNodes(run.TenantWorkers, run.Conformance); err != nil {
		return Config{}, err
	}
	if _, err := selectedNativeResources(run.Resources); err != nil {
		return Config{}, err
	}
	if run.Runs.Artifacts != "" {
		if !configString(run.Runs.Artifacts) {
			return Config{}, configUsage("artifacts Run path must be explicit")
		}
		for _, root := range []*string{&run.Runs.OS, &run.Runs.Components, &run.Runs.Providers, &run.Runs.Site, &run.Runs.Profiles} {
			if *root == "" {
				*root = run.Runs.Artifacts
			}
		}
	}
	switch run.Accelerator {
	case "", "hvf", "kvm", "tcg":
	default:
		return Config{}, configUsage("accelerator must be hvf, kvm or tcg")
	}
	network, err := normalizeNetwork(run.Network)
	if err != nil {
		return Config{}, err
	}
	if file.Version != 1 || (run.NVKECheckout != "" && !configString(run.NVKECheckout)) || !configString(run.Out) || run.TimeoutSeconds < MinTenantTimeoutSeconds || run.TimeoutSeconds > MaxTenantTimeoutSeconds {
		return Config{}, nvkrerr.New(nvkrerr.CodeUsage, "run requires version 1, out, valid optional nvkeCheckout and timeoutSeconds from %d to %d", MinTenantTimeoutSeconds, MaxTenantTimeoutSeconds)
	}
	return Config{Accelerator: run.Accelerator, Cleanup: run.Cleanup, Walkthrough: run.Walkthrough, Conformance: run.Conformance, TenantWorkers: run.TenantWorkers, Resources: run.Resources, Repository: run.Repo, Network: network, NVKECheckout: run.NVKECheckout, NVKR: run.NVKR, Out: run.Out, Timeout: time.Duration(run.TimeoutSeconds) * time.Second, Progress: progress,
		Runs: RunInputs{HostTools: run.Runs.HostTools, Runtime: run.Runs.Runtime, OS: run.Runs.OS, Components: run.Runs.Components, Providers: run.Runs.Providers, Site: run.Runs.Site, Tools: run.Runs.Tools, Profiles: run.Runs.Profiles, Simulator: run.Runs.Simulator}}, nil
}

// normalizeNetwork keeps NVIDIA OVN as the default. Alternatives are explicit
// local diagnostics; none is a fallback for an unavailable selected network.
func normalizeNetwork(network string) (string, error) {
	switch network {
	case "", "ovn":
		return "ovn", nil
	case "kind-calico", "upstream-ovn":
		return network, nil
	default:
		return "", configUsage("network must be ovn, upstream-ovn or kind-calico (explicit diagnostics)")
	}
}

func (file DemoFile) RunConfig(progress io.Writer) (Config, error) {
	result, err := file.runDescription(progress)
	if err != nil {
		return Config{}, err
	}
	for _, optional := range []string{result.Runs.HostTools, result.Runs.Runtime} {
		if optional != "" && !configString(optional) {
			return Config{}, configUsage("optional Run paths must be explicit")
		}
	}
	for _, value := range []string{result.NVKR, result.Runs.OS, result.Runs.Components, result.Runs.Providers, result.Runs.Site, result.Runs.Tools, result.Runs.Profiles} {
		if !configString(value) {
			return Config{}, configUsage("run requires nvkr, artifact Runs and a native tools Run")
		}
	}
	return result, nil
}

func (file DemoFile) RestoreWork() (string, error) {
	if file.Version != 1 || !configString(file.Restore.Work) {
		return "", configUsage("restore requires version 1 and work")
	}
	return file.Restore.Work, nil
}

// E2EConfig reserves artifact paths for the newly completed build result. A
// configured existing Run or nvkr cannot silently substitute retained bytes.
func (file DemoFile) E2EConfig(progress io.Writer) (BuildConfig, Config, error) {
	build, buildErr := file.BuildConfig(progress)
	run, runErr := file.runDescription(progress)
	if err := errors.Join(buildErr, runErr); err != nil {
		return BuildConfig{}, Config{}, err
	}
	if file.Run.NVKR != "" || file.Run.Runs != (RunFileInputs{}) {
		return BuildConfig{}, Config{}, configUsage("e2e cannot configure nvkr or existing Runs")
	}
	build.Network = run.Network
	if run.Repository != "" && run.Repository != build.Repository {
		return BuildConfig{}, Config{}, configUsage("e2e uses build.repo as the single source repository")
	}
	run.Repository = build.Repository
	return build, run, nil
}
