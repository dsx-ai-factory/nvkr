// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package conformance runs an explicitly requested, pinned upstream suite on an
// existing cluster. Its API observations describe what that cluster reported;
// they are not authenticated installed-byte or qualification receipts.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"sigs.k8s.io/yaml"
)

const maxToolBytes = 256 << 20
const maxAPIBytes = 16 << 20
const maxDiagnosticBytes = 1 << 20

type Tool struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type Options struct {
	Hydrophone        ExecutableSelector              `json:"hydrophone"`
	Kubectl           ExecutableSelector              `json:"kubectl"`
	Kubeconfig        Tool                            `json:"kubeconfig"`
	Context           string                          `json:"context"`
	CNINamespace      string                          `json:"cniNamespace,omitempty"`
	CNISelector       string                          `json:"cniSelector"`
	Image             string                          `json:"-"`
	ConformanceImage  ImageSelector                   `json:"conformanceImage"`
	ImageVersion      string                          `json:"imageVersion"`
	BusyboxImage      string                          `json:"-"`
	Busybox           ImageSelector                   `json:"busybox"`
	SuiteName         string                          `json:"suiteName"`
	Role              string                          `json:"role"`
	WorkspaceRoot     string                          `json:"-"`
	Environment       []string                        `json:"environment"`
	RunTimeout        time.Duration                   `json:"-"`
	CommandTimeout    time.Duration                   `json:"-"`
	CleanupTimeout    time.Duration                   `json:"-"`
	StartupTimeout    time.Duration                   `json:"-"`
	DisposableCluster bool                            `json:"disposableCluster"`
	ToolArtifacts     *lifecycle.ArtifactCheckReceipt `json:"-"`
	Artifacts         *lifecycle.ArtifactCheckReceipt `json:"-"`
	Targets           []DeploymentTarget              `json:"targets"`
	localImageAliases map[string]string
	localDiagnostic   bool
}

type admitted struct {
	options                         Options
	hydrophone, kubectl, kubeconfig *heldfile.File
	kubeBytes                       []byte
	native                          nativeExecution
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

// Darwin snapshots are restricted to captured local diagnostics. Published,
// signing and qualification execution retain Linux held-descriptor admission.
func requireExecutionSupport(local bool) error {
	if local && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return nil
	}
	return heldfile.RequireExecutableSupport()
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
	validated, err := validateOptions(o)
	if err != nil {
		return nil, err
	}
	o = validated
	if err = requireExecutionSupport(o.localDiagnostic); err != nil {
		return nil, capability(err)
	}
	a := &admitted{options: o}
	complete := false
	defer func() {
		if !complete {
			a.close()
		}
	}()
	a.kubeconfig, err = openPrivateKubeconfig(ctx, o.Kubeconfig)
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
func (a *admitted) close() {
	for _, f := range []*heldfile.File{a.hydrophone, a.kubectl, a.kubeconfig} {
		if f != nil {
			_ = f.Close()
		}
	}
}

func (a *admitted) closeNative() error { return a.native.close() }

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
	if err := json.Unmarshal(raw, out); err != nil {
		return failure("invalid cluster API JSON")
	}
	return nil
}

func validateOptions(o Options) (Options, error) {
	if err := validateTargetShape(o.Targets); err != nil {
		return Options{}, err
	}
	o.Targets = append([]DeploymentTarget(nil), o.Targets...)
	if o.Role != "local" {
		return Options{}, e.New(e.CodeEngineCapability, "conformance execution is admitted only for local diagnostic clusters; upstream name-based automatic cleanup prevents site and tenant admission")
	}
	if !o.DisposableCluster {
		return Options{}, e.New(e.CodeUsage, "conformance requires explicit disposable-cluster acknowledgement")
	}
	if len(o.Environment) != 0 {
		return Options{}, e.New(e.CodeUnsupported, "conformance profile does not admit caller environment overrides; configure the supported tool flags and embedded kubeconfig")
	}
	if !safetext.Valid(o.SuiteName, 256, false) || strings.TrimSpace(o.SuiteName) != o.SuiteName || !safetext.Valid(o.WorkspaceRoot, 4096, false) {
		return Options{}, e.New(e.CodeUsage, "conformance requires named suite and private workspace root")
	}
	if o.CNINamespace == "" {
		o.CNINamespace = "kube-system"
	}
	if !safetext.Valid(o.CNINamespace, 256, false) || strings.ContainsAny(o.CNINamespace, "/ \t") {
		return Options{}, e.New(e.CodeUsage, "conformance requires a valid explicit CNI namespace")
	}
	if o.Context == "" || len(o.Context) > 4096 || strings.ContainsAny(o.Context, "\x00\r\n") || o.CNISelector == "" || len(o.CNISelector) > 4096 || strings.ContainsAny(o.CNISelector, "\x00\r\n") {
		return Options{}, e.New(e.CodeUsage, "conformance: explicit private context and CNI selector required")
	}
	if o.StartupTimeout == 0 {
		o.StartupTimeout = 5 * time.Minute
	}
	if o.StartupTimeout <= 0 || o.StartupTimeout > 12*time.Hour {
		return Options{}, e.New(e.CodeUsage, "conformance: startup timeout must be positive and at most 12 hours")
	}
	if o.RunTimeout <= 0 || o.RunTimeout > 12*time.Hour || o.CommandTimeout <= 0 || o.CommandTimeout > 5*time.Minute || o.CleanupTimeout <= 0 || o.CleanupTimeout > 10*time.Minute {
		return Options{}, e.New(e.CodeUsage, "conformance: positive bounded timeouts required")
	}
	if o.Kubeconfig.Path == "" || !canonical.IsDigest(o.Kubeconfig.Digest) {
		return Options{}, e.New(e.CodeUnpinnedInput, "conformance requires an exact kubeconfig access pin")
	}
	for _, selector := range []ExecutableSelector{o.Hydrophone, o.Kubectl} {
		if err := validateExecutableSelectorFor(selector, o.localDiagnostic); err != nil {
			return Options{}, err
		}
	}
	for _, selector := range []ImageSelector{o.ConformanceImage, o.Busybox} {
		if err := validateImageSelector(selector); err != nil {
			return Options{}, err
		}
	}
	version, err := semver.StrictNewVersion(strings.TrimPrefix(o.ImageVersion, "v"))
	if err != nil || version.Prerelease() != "" || version.Metadata() != "" {
		return Options{}, e.New(e.CodeUsage, "conformance: stable declared image version required")
	}

	return o, nil
}
