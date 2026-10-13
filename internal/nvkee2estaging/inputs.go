// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkee2estaging

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const documentLimit int64 = 16 << 20
const fixtureLimit int64 = 64 << 30

var chartRoles = []string{"controller", "postgresql", "temporal", "spire", "spire-crds", "nktp"}
var imageRoles = []string{"controller", "capi-provisioned", "profiles"}
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var positiveDecimal = regexp.MustCompile(`^[1-9][0-9]*$`)

type mapping struct {
	SuiteConfig map[string]json.RawMessage `json:"suite_config"`
	Bindings    struct {
		CLI    binding            `json:"cli"`
		Images map[string]binding `json:"images"`
		Charts map[string]binding `json:"charts"`
	} `json:"bindings"`
	DeployFixture struct {
		Commit   string `json:"commit"`
		Identity string `json:"identity"`
	} `json:"deploy_fixture,omitempty"`
}

func readDocument(ctx context.Context, filename string, value any) error {
	file, info, err := securepath.OpenRegular(filename)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "opening preparation input")
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > documentLimit {
		return e.New(e.CodeUsage, "preparation document exceeds its 16 MiB bound")
	}
	var raw bytes.Buffer
	if _, err := transportarchive.CopyExact(ctx, &raw, file, info.Size()); err != nil {
		return inputError(ctx, e.CodeUsage, err, "reading preparation input")
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) {
		return e.New(e.CodeVerifyFailed, "preparation input changed while reading")
	}
	// The existing codec owns duplicate-name/UTF-8/trailing-document validation.
	if err := canonical.ValidateJSONDocument(raw.Bytes()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return e.Wrap(e.CodeUsage, err, "decoding preparation input")
	}
	return nil
}

func invocation(options Options) (string, error) {
	server := options.GitHubServerURL
	if server == "" {
		server = "https://github.com"
	}
	parsed, err := url.Parse(server)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", e.New(e.CodeUsage, "GitHub server URL must be an explicit HTTPS origin without credentials")
	}
	if !repositoryPattern.MatchString(options.Repository) || !positiveDecimal.MatchString(options.RunID) || !positiveDecimal.MatchString(options.RunAttempt) {
		return "", e.New(e.CodeUsage, "repository and producing Run/attempt must be explicit canonical GitHub identifiers")
	}
	return strings.TrimSuffix(server, "/") + "/" + options.Repository + "/actions/runs/" + options.RunID + "/attempts/" + options.RunAttempt, nil
}

func checkRun(run lifecycle.Run, options Options) (map[string]lifecycle.Artifact, map[string]string, error) {
	if run.SchemaVersion != lifecycle.RunSchemaVersion || run.Attempt != options.RunID+"-"+options.RunAttempt || len(run.Expected) == 0 || len(run.Expected) != len(run.Observed) {
		return nil, nil, e.New(e.CodeVerifyFailed, "require current Run identities with a consistent observed output description")
	}
	identities := map[string]string{"definitionDigest": run.DefinitionDigest, "environmentDigest": run.EnvironmentDigest, "planDigest": run.PlanDigest, "contentSubject": run.ContentSubject}
	for _, digest := range identities {
		if !canonical.IsDigest(digest) {
			return nil, nil, e.New(e.CodeVerifyFailed, "Run omits exact input/content identities")
		}
	}
	named := make(map[string]lifecycle.Artifact, len(run.Observed))
	for _, artifact := range run.Observed {
		if err := artifact.Validate(); err != nil {
			return nil, nil, err
		}
		if _, exists := named[artifact.Name]; exists {
			return nil, nil, e.New(e.CodeVerifyFailed, "Run duplicates observed outputs")
		}
		named[artifact.Name] = artifact
	}
	seen := make(map[string]bool)
	for _, expected := range run.Expected {
		actual, exists := named[expected.Name]
		if seen[expected.Name] || !exists || expected.Kind != actual.Kind || expected.BuildDigest != actual.BuildDigest || !slices.Equal(expected.Components, actual.Components) {
			return nil, nil, e.New(e.CodeVerifyFailed, "Run contains missing, duplicated or substituted output bindings")
		}
		seen[expected.Name] = true
	}
	return named, identities, nil
}

// Guard only downstream safety/material fields, leaving product-owned config
// opaque. EqualFold matches Go's JSON Unicode matching; aliases never survive.
func guardObject(object map[string]json.RawMessage, protected ...string) error {
	if object == nil {
		return e.New(e.CodeUsage, "suite configuration sections must be JSON objects")
	}
	for key := range object {
		for _, field := range protected {
			if strings.EqualFold(key, field) && key != field {
				return e.New(e.CodeUsage, "suite field must use canonical casing: %s", field)
			}
		}
	}
	return nil
}

func section(config map[string]json.RawMessage, name string, optional bool) (map[string]json.RawMessage, error) {
	raw, exists := config[name]
	if !exists && optional {
		return map[string]json.RawMessage{}, nil
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, e.New(e.CodeUsage, "suite section %q must be a JSON object", name)
	}
	return result, nil
}

func checkedMapping(input mapping) (string, error) {
	if err := guardObject(input.SuiteConfig, "site", "lifecycle", "nvkectl"); err != nil {
		return "", err
	}
	lifecycleConfig, err := section(input.SuiteConfig, "lifecycle", true)
	if err != nil {
		return "", err
	}
	if err := guardObject(lifecycleConfig, "skipDelete", "preserveOnFailure", "reuseCluster"); err != nil {
		return "", err
	}
	for _, key := range []string{"skipDelete", "preserveOnFailure"} {
		var preserve bool
		if raw, exists := lifecycleConfig[key]; exists {
			if err := json.Unmarshal(raw, &preserve); err != nil {
				return "", e.New(e.CodeUsage, "lifecycle preservation setting must be a boolean")
			}
		}
		if preserve {
			return "", e.New(e.CodeUsage, "E2E inputs must not preserve or reuse hardware")
		}
	}
	var reuse string
	if raw, exists := lifecycleConfig["reuseCluster"]; exists {
		if err := json.Unmarshal(raw, &reuse); err != nil || reuse != "" {
			return "", e.New(e.CodeUsage, "E2E inputs must not reuse hardware")
		}
	}
	site, err := section(input.SuiteConfig, "site", false)
	if err != nil {
		return "", err
	}
	if err := guardObject(site, "deployPathRef", "images", "chartOverrides"); err != nil {
		return "", err
	}
	var commit string
	if err := json.Unmarshal(site["deployPathRef"], &commit); err != nil || !commitPattern.MatchString(commit) {
		return "", e.New(e.CodeUsage, "deployment assets require an explicit pinned source commit")
	}
	cliConfig, err := section(input.SuiteConfig, "nvkectl", true)
	if err != nil {
		return "", err
	}
	if err := guardObject(cliConfig, "binary"); err != nil {
		return "", err
	}
	if len(input.Bindings.Images) != len(imageRoles) || len(input.Bindings.Charts) != len(chartRoles) {
		return "", e.New(e.CodeUsage, "every installed image and chart needs one candidate or explicit fixture binding")
	}
	all := []binding{input.Bindings.CLI}
	for _, role := range imageRoles {
		b, exists := input.Bindings.Images[role]
		if !exists {
			return "", e.New(e.CodeUsage, "missing site image binding")
		}
		all = append(all, b)
		if b.Fixture != nil {
			if strings.TrimSpace(b.Repository) == "" || !canonical.IsDigest(b.Digest) {
				return "", e.New(e.CodeUsage, "image fixture requires repository and native digest")
			}
		}
	}
	for _, role := range chartRoles {
		b, exists := input.Bindings.Charts[role]
		if !exists {
			return "", e.New(e.CodeUsage, "missing site chart binding")
		}
		all = append(all, b)
	}
	for _, b := range all {
		if (b.Output != "") == (b.Fixture != nil) {
			return "", e.New(e.CodeUsage, "each role needs exactly one output or fixture")
		}
		if b.Fixture != nil && (strings.TrimSpace(b.Fixture.Identity) == "" || b.Fixture.Path == "" || !canonical.IsDigest(b.Fixture.SHA256)) {
			return "", e.New(e.CodeUsage, "fixtures require explicit material identity, regular file and byte pin")
		}
	}
	cli := input.Bindings.CLI
	if cli.Fixture != nil || cli.InstallPath == "" || !path.IsAbs(cli.InstallPath) || path.Clean(cli.InstallPath) != cli.InstallPath || strings.ContainsAny(cli.InstallPath, "\\\x00") {
		return "", e.New(e.CodeUsage, "CLI must select a producing output and canonical absolute installation path")
	}
	return commit, nil
}

func checkOutputs(named map[string]lifecycle.Artifact, release lifecycle.Release, identities map[string]string, exact lifecycle.ExactInputDescription, reference string) error {
	if release.Subject != reference || exact.ReleaseSubject != reference || exact.DefinitionDigest != identities["definitionDigest"] || exact.EnvironmentDigest != identities["environmentDigest"] || exact.PlanDigest != identities["planDigest"] || exact.ContentSubject != identities["contentSubject"] {
		return e.New(e.CodeVerifyFailed, "verified publication differs from producing Run identities")
	}
	if len(named) != len(release.Artifacts) {
		return e.New(e.CodeVerifyFailed, "verified publication differs from complete Run membership")
	}
	seen := map[string]bool{}
	for _, output := range release.Artifacts {
		actual, exists := named[output.Name]
		if !exists || seen[output.Name] || output.Digest != actual.Digest || output.Kind != actual.Kind || output.BuildDigest != actual.BuildDigest || !slices.Equal(output.Components, actual.Components) {
			return e.New(e.CodeVerifyFailed, "verified publication substitutes a producing Run output")
		}
		seen[output.Name] = true
	}
	return nil
}

// Run descriptions may supply untrusted transport/platform metadata. Material
// selection always uses the projection supplied by the verified Release owner.
func checkedArtifacts(named map[string]lifecycle.Artifact, release lifecycle.Release, identities map[string]string, exact lifecycle.ExactInputDescription, reference string) (map[string]lifecycle.Artifact, error) {
	if err := checkOutputs(named, release, identities, exact, reference); err != nil {
		return nil, err
	}
	result := make(map[string]lifecycle.Artifact, len(release.Artifacts))
	for _, artifact := range release.Artifacts {
		result[artifact.Name] = artifact
	}
	return result, nil
}

func selected(named map[string]lifecycle.Artifact, b binding, kind string) (lifecycle.Artifact, error) {
	output, exists := named[b.Output]
	if !exists || output.Kind != kind {
		return lifecycle.Artifact{}, e.New(e.CodeVerifyFailed, "binding does not select a published %s output", kind)
	}
	return output, nil
}

func validateReference(reference string) error {
	return delivery.ValidatePublicationReference(reference)
}
