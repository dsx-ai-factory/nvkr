// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package conformance runs an explicitly requested, pinned upstream suite on an
// existing cluster. Its API observations describe what that cluster reported;
// they are not authenticated installed-byte or qualification receipts.
package conformance

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/google/go-containerregistry/pkg/name"
	"sigs.k8s.io/yaml"
)

const HarnessRevision = "5fe47daaff5fe025215cb257118dc2a0e643e1d6"
const maxToolBytes = 256 << 20
const maxAPIBytes = 16 << 20
const maxDiagnosticBytes = 1 << 20

type Tool struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type Options struct {
	Hydrophone, Kubectl                        Tool
	Kubeconfig                                 Tool
	Context                                    string
	CNISelector                                string
	Image, ImageVersion, BusyboxImage          string
	Observation                                testobservation.Observation
	RunTimeout, CommandTimeout, CleanupTimeout time.Duration
	StartupTimeout                             time.Duration
	DisposableCluster                          bool
}
type admitted struct {
	options                         Options
	hydrophone, kubectl, kubeconfig *heldfile.File
	kubeBytes                       []byte
}

func failure(reason string) error { return e.New(e.CodeVerifyFailed, "conformance: %s", reason) }

// Capture cancellation keeps its lifecycle code and wrapped cause at every I/O boundary.
func captureFailure(ctx context.Context, err error, code e.Code, message string) error {
	if ctx != nil && ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s", message)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return e.Wrap(e.CodeTimeout, err, "%s", message)
	}
	return e.Wrap(code, err, "%s", message)
}
func capability(err error) error {
	return e.Wrap(e.CodeEngineCapability, err, "conformance requires held Linux executable support")
}
func admit(ctx context.Context, o Options) (*admitted, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "conformance requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "conformance canceled before admission")
	}
	if o.Observation.Environment.Role != "site" && o.Observation.Environment.Role != "tenant" && o.Observation.Environment.Role != "local" {
		return nil, e.New(e.CodeUsage, "conformance: explicit site, tenant or local role declaration required")
	}
	if o.Context == "" || len(o.Context) > 4096 || strings.ContainsAny(o.Context, "\x00\r\n") || o.CNISelector == "" || len(o.CNISelector) > 4096 || strings.ContainsAny(o.CNISelector, "\x00\r\n") {
		return nil, e.New(e.CodeUsage, "conformance: explicit private context and CNI selector required")
	}
	if o.StartupTimeout == 0 {
		o.StartupTimeout = 5 * time.Minute
	}
	if o.StartupTimeout <= 0 || o.StartupTimeout > 12*time.Hour {
		return nil, e.New(e.CodeUsage, "conformance: startup timeout must be positive and at most 12 hours")
	}
	if o.RunTimeout <= 0 || o.RunTimeout > 12*time.Hour || o.CommandTimeout <= 0 || o.CommandTimeout > 5*time.Minute || o.CleanupTimeout <= 0 || o.CleanupTimeout > 10*time.Minute {
		return nil, e.New(e.CodeUsage, "conformance: positive bounded timeouts required")
	}
	for _, tool := range []Tool{o.Hydrophone, o.Kubectl, o.Kubeconfig} {
		if tool.Path == "" || !canonical.IsDigest(tool.Digest) {
			return nil, e.New(e.CodeUnpinnedInput, "conformance: exact executable and kubeconfig byte pins required")
		}
	}
	image, err := name.NewDigest(o.Image, name.StrictValidation)
	if err != nil || image.Context().Name() != "registry.k8s.io/conformance" {
		return nil, e.New(e.CodeUnpinnedInput, "conformance: upstream conformance image requires digest reference")
	}
	if sidecar, sidecarErr := name.NewDigest(o.BusyboxImage, name.StrictValidation); sidecarErr != nil || !canonical.IsDigest(sidecar.DigestStr()) {
		return nil, e.New(e.CodeUnpinnedInput, "conformance: busybox sidecar requires digest reference")
	}
	version, err := semver.StrictNewVersion(strings.TrimPrefix(o.ImageVersion, "v"))
	if err != nil || version.Prerelease() != "" || version.Metadata() != "" {
		return nil, e.New(e.CodeUsage, "conformance: stable declared image version required")
	}
	if o.Observation.Suite.HarnessRevision != HarnessRevision {
		return nil, e.New(e.CodeUnpinnedInput, "conformance: declared harness revision must match the admitted upstream revision")
	}
	if o.Observation.Suite.ImageDigest != image.DigestStr() {
		return nil, e.New(e.CodeUnpinnedInput, "conformance: suite image digest must match selected image")
	}
	if len(o.Observation.Suite.Selection) != 1 || o.Observation.Suite.Selection[0] != `\[Conformance\]` {
		return nil, e.New(e.CodeUsage, "conformance: full conformance selection required")
	}
	if len(o.Observation.Suite.RequiredCases) == 0 {
		return nil, e.New(e.CodeUsage, "conformance: independently selected required cases must be nonempty")
	}
	if err = testobservation.ValidateDescription(ctx, o.Observation); err != nil {
		return nil, err
	}
	if err = heldfile.RequireExecutableSupport(); err != nil {
		return nil, capability(err)
	}
	a := &admitted{options: o}
	complete := false
	defer func() {
		if !complete {
			a.close()
		}
	}()
	a.hydrophone, err = openTool(ctx, o.Hydrophone)
	if err != nil {
		return nil, err
	}
	a.kubectl, err = openTool(ctx, o.Kubectl)
	if err != nil {
		return nil, err
	}
	a.kubeconfig, err = heldfile.OpenContext(ctx, o.Kubeconfig.Path, heldfile.Options{Label: "private kubeconfig", MaxBytes: 1 << 20, ExpectedDigest: o.Kubeconfig.Digest, RequirePrivateMode: true})
	if err != nil {
		return nil, captureFailure(ctx, err, e.CodeUnpinnedInput, "holding private kubeconfig")
	}
	a.kubeBytes, err = a.kubeconfig.ReadAllContext(ctx, 1<<20)
	if err != nil {
		return nil, captureFailure(ctx, err, e.CodeVerifyFailed, "reading held kubeconfig")
	}
	if err = checkKubeconfig(a.kubeBytes, o.Context); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "conformance canceled after capture")
	}
	complete = true
	return a, nil
}
func openTool(ctx context.Context, tool Tool) (*heldfile.File, error) {
	held, err := heldfile.OpenContext(ctx, tool.Path, heldfile.Options{Label: "conformance tool", Executable: true, MaxBytes: maxToolBytes, ExpectedDigest: tool.Digest})
	if err != nil {
		return nil, captureFailure(ctx, err, e.CodeUnpinnedInput, "holding pinned conformance executable")
	}
	parsed, err := elf.NewFile(held)
	if err != nil {
		_ = held.Close()
		return nil, e.New(e.CodeEngineCapability, "conformance tool must be static Linux ELF")
	}
	defer func() { _ = parsed.Close() }()
	native := (runtime.GOARCH == "amd64" && parsed.Machine == elf.EM_X86_64) || (runtime.GOARCH == "arm64" && parsed.Machine == elf.EM_AARCH64)
	if !native || parsed.Type != elf.ET_EXEC || parsed.Class != elf.ELFCLASS64 || parsed.Data != elf.ELFDATA2LSB {
		_ = held.Close()
		return nil, e.New(e.CodeEngineCapability, "conformance tool requires native 64-bit static Linux ELF")
	}

	for _, p := range parsed.Progs {
		if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
			_ = held.Close()
			return nil, e.New(e.CodeEngineCapability, "conformance dynamic executable unsupported")
		}
	}
	return held, nil
}
func (a *admitted) close() {
	for _, f := range []*heldfile.File{a.hydrophone, a.kubectl, a.kubeconfig} {
		if f != nil {
			_ = f.Close()
		}
	}
}

// Kubeconfig syntax is owned by the established sigs.k8s.io/yaml decoder.
// This deliberately narrow admission profile admits only self-contained static
// credentials; client-go in the pinned executable owns connection semantics.
func checkKubeconfig(raw []byte, want string) error {
	document, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return failure("invalid private kubeconfig YAML")
	}
	if err = canonical.ValidateJSONDocument(document); err != nil {
		return failure("ambiguous private kubeconfig")
	}
	var config struct {
		APIVersion  string          `json:"apiVersion"`
		Kind        string          `json:"kind"`
		Current     string          `json:"current-context"`
		Preferences json.RawMessage `json:"preferences"`
		Clusters    []struct {
			Name    string `json:"name"`
			Cluster struct {
				Server  string `json:"server"`
				CA      string `json:"certificate-authority-data"`
				TLSName string `json:"tls-server-name"`
			} `json:"cluster"`
		} `json:"clusters"`
		Users []struct {
			Name string `json:"name"`
			User struct {
				Token       string `json:"token"`
				Certificate string `json:"client-certificate-data"`
				Key         string `json:"client-key-data"`
			} `json:"user"`
		} `json:"users"`
		Contexts []struct {
			Name    string `json:"name"`
			Context struct {
				Cluster   string `json:"cluster"`
				User      string `json:"user"`
				Namespace string `json:"namespace"`
			} `json:"context"`
		} `json:"contexts"`
	}
	d := json.NewDecoder(bytes.NewReader(document))
	d.DisallowUnknownFields()
	if err = d.Decode(&config); err != nil {
		return failure("kubeconfig requires embedded credentials without external files or plugins")
	}
	if config.APIVersion != "v1" || config.Kind != "Config" || config.Current != want || len(config.Clusters) != 1 || len(config.Users) != 1 || len(config.Contexts) != 1 {
		return failure("single exact kubeconfig context required")
	}
	c, u, x := config.Clusters[0], config.Users[0], config.Contexts[0]
	if x.Name != want || x.Context.Cluster != c.Name || x.Context.User != u.Name || !strings.HasPrefix(c.Cluster.Server, "https://") || c.Cluster.CA == "" || ((u.User.Token == "") == (u.User.Certificate == "" || u.User.Key == "")) {
		return failure("kubeconfig requires exact TLS target and one embedded authentication method")
	}
	if u.User.Token != "" && (u.User.Certificate != "" || u.User.Key != "") {
		return failure("kubeconfig has multiple authentication methods")
	}
	if len(config.Preferences) > 0 && string(config.Preferences) != "{}" && string(config.Preferences) != "null" {
		return failure("kubeconfig preferences unsupported")
	}
	return nil
}
func decodeAPI(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > maxAPIBytes {
		return failure("cluster API response byte budget")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return failure("ambiguous cluster API JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(out); err != nil {
		return failure("invalid cluster API JSON")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return failure("trailing cluster API JSON")
	}
	return nil
}
