// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validationplan

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

const (
	// ConfigAPIVersion and ConfigKind identify the planners configuration.
	ConfigAPIVersion = "nvkr.dev/validation-planners/v1"
	ConfigKind       = "ValidationPlanners"

	// ConfigFile is the file a planners directory holds.
	ConfigFile = "config.yaml"

	configByteLimit = 1 << 20
	maxPlanners     = 16
)

// Config lists the planners a validation plan is built from, each paired with
// the executor that runs its targets and the planner's own configuration file.
type Config struct {
	APIVersion string  `yaml:"apiVersion"`
	Kind       string  `yaml:"kind"`
	Planners   []Entry `yaml:"planners"`
}

// Entry is one planner invocation.
type Entry struct {
	// Name is the built-in planner to run.
	Name string `yaml:"name"`

	// Executor names who runs the targets this planner makes.
	Executor string `yaml:"executor"`

	// Config is the planner's own file, relative to the planners directory.
	Config string `yaml:"config"`
}

// LoadConfig reads and validates dir/config.yaml. Every failure is a coded
// error and nothing is returned with it.
func LoadConfig(dir string) (Config, error) {
	if strings.TrimSpace(dir) == "" {
		return Config{}, e.New(e.CodeUsage, "validation planning requires a planners directory")
	}
	path := filepath.Join(dir, ConfigFile)
	raw, err := readBounded(path, configByteLimit)
	if err != nil {
		return Config{}, e.Wrap(e.CodeUsage, err, "reading the planners configuration")
	}
	cfg, err := decodeConfig(raw)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, e.New(e.CodeUsage, "%s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, e.New(e.CodeUsage, "%s exceeds %d bytes", filepath.Base(path), limit)
	}
	return raw, nil
}

func decodeConfig(raw []byte) (Config, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return Config{}, e.Wrap(e.CodeUsage, err, "decoding the planners configuration")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, e.New(e.CodeUsage, "the planners configuration requires exactly one document")
	}
	if err := rejectUnsafeNodes(&node, 0); err != nil {
		return Config{}, err
	}
	var cfg Config
	strict := yaml.NewDecoder(bytes.NewReader(raw))
	strict.KnownFields(true)
	if err := strict.Decode(&cfg); err != nil {
		return Config{}, e.Wrap(e.CodeUsage, err, "invalid planners configuration fields")
	}
	return cfg, nil
}

// rejectUnsafeNodes refuses aliases, anchors, merge keys, repeated keys and
// excessive nesting, so one file has one reading.
func rejectUnsafeNodes(n *yaml.Node, depth int) error {
	if depth > 32 || n.Kind == yaml.AliasNode || n.Anchor != "" {
		return e.New(e.CodeUsage, "aliases, anchors and excessive nesting are forbidden in the planners configuration")
	}
	if n.Kind == yaml.MappingNode {
		keys := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Value == "<<" || keys[k.Value] {
				return e.New(e.CodeUsage, "the planners configuration has a duplicate or invalid key")
			}
			keys[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := rejectUnsafeNodes(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validate() error {
	if c.APIVersion != ConfigAPIVersion || c.Kind != ConfigKind {
		return e.New(e.CodeUnsupported, "planners configuration must be %s %s", ConfigAPIVersion, ConfigKind)
	}
	if len(c.Planners) == 0 || len(c.Planners) > maxPlanners {
		return e.New(e.CodeUsage, "planners configuration requires 1..%d planners", maxPlanners)
	}
	seen := make(map[string]bool, len(c.Planners))
	for _, entry := range c.Planners {
		if entry.Name == "" || strings.TrimSpace(entry.Name) != entry.Name {
			return e.New(e.CodeUsage, "a planner entry requires a name")
		}
		if seen[entry.Name] {
			return e.New(e.CodeUsage, "planner %q is listed more than once", entry.Name)
		}
		seen[entry.Name] = true
		if !executorKnown(entry.Executor) {
			return e.New(e.CodeUnsupported, "planner %q names executor %q, which is not known", entry.Name, entry.Executor)
		}
		if !filepath.IsLocal(entry.Config) {
			return e.New(e.CodeUsage, "planner %q config must be a path inside the planners directory", entry.Name)
		}
	}
	return nil
}
