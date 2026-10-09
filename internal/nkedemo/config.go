// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

const demoFileLimit = 64 << 10

// DemoFile is downstream deployment intent, not an NKD Definition or receipt.
// Loading descriptions never builds, deploys, restores, or writes configuration.
type DemoFile struct {
	Version int         `yaml:"version" mapstructure:"version"`
	Build   BuildFile   `yaml:"build" mapstructure:"build"`
	Run     RunFile     `yaml:"run" mapstructure:"run"`
	Restore RestoreFile `yaml:"restore" mapstructure:"restore"`
}

type BuildFile struct {
	Repo     string `yaml:"repo" mapstructure:"repo"`
	NKEClone string `yaml:"nkeClone" mapstructure:"nkeClone"`
	Attempt  string `yaml:"attempt" mapstructure:"attempt"`
	Jobs     int    `yaml:"jobs" mapstructure:"jobs"`
}

type RunFile struct {
	NKECheckout    string        `yaml:"nkeCheckout" mapstructure:"nkeCheckout"`
	NKDCTL         string        `yaml:"nkdctl" mapstructure:"nkdctl"`
	Runs           RunFileInputs `yaml:"runs" mapstructure:"runs"`
	Out            string        `yaml:"out" mapstructure:"out"`
	TimeoutSeconds int           `yaml:"timeoutSeconds" mapstructure:"timeoutSeconds"`
}

type RunFileInputs struct {
	OS         string `yaml:"os" mapstructure:"os"`
	Components string `yaml:"components" mapstructure:"components"`
	Providers  string `yaml:"providers" mapstructure:"providers"`
	Site       string `yaml:"site" mapstructure:"site"`
	Tools      string `yaml:"tools" mapstructure:"tools"`
	Profiles   string `yaml:"profiles" mapstructure:"profiles"`
	Simulator  string `yaml:"simulator" mapstructure:"simulator"`
}

type RestoreFile struct {
	Work string `yaml:"work" mapstructure:"work"`
}

// LoadDemoFile uses only the explicitly selected regular file and an independent
// Viper instance. There is no config search, environment binding, watcher,
// remote provider, or configuration writer. Paths stay descriptions until the
// selected operation's existing admission checks validate them.
func LoadDemoFile(ctx context.Context, path string) (DemoFile, error) {
	var result DemoFile
	if err := requireContext(ctx); err != nil {
		return result, nkderr.Wrap(nkderr.CodeUsage, err, "demo configuration context")
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
			return result, nkderr.Wrap(nkderr.CodeUsage, ctx.Err(), "demo configuration context")
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
	// inspect its parsed tags too: this schema contains only maps, strings and
	// integers. No YAML codec or scalar grammar is implemented here.
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
	loader := viper.New()
	loader.SetConfigType("yaml")
	loader.SetDefault("build.jobs", 1)
	loader.SetDefault("run.timeoutSeconds", 3600)
	if err = loader.ReadConfig(bytes.NewReader(raw)); err != nil {
		return result, configUsage("cannot decode demo configuration")
	}
	if err = loader.UnmarshalExact(&result, func(cfg *mapstructure.DecoderConfig) {
		cfg.WeaklyTypedInput = false
		cfg.DecodeHook = nil
	}); err != nil {
		return DemoFile{}, configUsage("invalid demo configuration schema or types")
	}
	if err = requireContext(ctx); err != nil {
		return DemoFile{}, nkderr.Wrap(nkderr.CodeUsage, err, "demo configuration context")
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
		return node.Tag == "!!str" || node.Tag == "!!int"
	default:
		return false
	}
}

func configUsage(message string) error { return nkderr.New(nkderr.CodeUsage, "%s", message) }

func configString(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\x00\r\n")
}

func (file DemoFile) BuildConfig(progress io.Writer) (BuildConfig, error) {
	build := file.Build
	if file.Version != 1 || !configString(build.Repo) || !configString(build.Attempt) || build.Jobs < 1 || (build.NKEClone != "" && !configString(build.NKEClone)) {
		return BuildConfig{}, configUsage("build requires version 1, repo, attempt and positive jobs")
	}
	return BuildConfig{Repository: build.Repo, NKEClone: build.NKEClone, AttemptPrefix: build.Attempt, Jobs: build.Jobs, Progress: progress}, nil
}

func (file DemoFile) runDescription(progress io.Writer) (Config, error) {
	run := file.Run
	if file.Version != 1 || (run.NKECheckout != "" && !configString(run.NKECheckout)) || !configString(run.Out) || run.TimeoutSeconds < 300 || run.TimeoutSeconds > 3600 {
		return Config{}, configUsage("run requires version 1, out, valid optional nkeCheckout and timeoutSeconds from 300 to 3600")
	}
	return Config{NKECheckout: run.NKECheckout, NKDCTL: run.NKDCTL, Out: run.Out, Timeout: time.Duration(run.TimeoutSeconds) * time.Second, Progress: progress,
		Runs: RunInputs{OS: run.Runs.OS, Components: run.Runs.Components, Providers: run.Runs.Providers, Site: run.Runs.Site, Tools: run.Runs.Tools, Profiles: run.Runs.Profiles, Simulator: run.Runs.Simulator}}, nil
}

func (file DemoFile) RunConfig(progress io.Writer) (Config, error) {
	result, err := file.runDescription(progress)
	if err != nil {
		return Config{}, err
	}
	if !configString(result.NKECheckout) {
		return Config{}, configUsage("replay run requires an explicit nkeCheckout")
	}
	for _, value := range []string{result.NKDCTL, result.Runs.OS, result.Runs.Components, result.Runs.Providers, result.Runs.Site, result.Runs.Tools, result.Runs.Profiles, result.Runs.Simulator} {
		if !configString(value) {
			return Config{}, configUsage("run requires nkdctl and every completed Run path")
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
// configured existing Run or nkdctl cannot silently substitute retained bytes.
func (file DemoFile) E2EConfig(progress io.Writer) (BuildConfig, Config, error) {
	build, buildErr := file.BuildConfig(progress)
	run, runErr := file.runDescription(progress)
	if err := errors.Join(buildErr, runErr); err != nil {
		return BuildConfig{}, Config{}, err
	}
	if file.Run.NKDCTL != "" || file.Run.Runs != (RunFileInputs{}) {
		return BuildConfig{}, Config{}, configUsage("e2e cannot configure nkdctl or existing Runs")
	}
	return build, run, nil
}
