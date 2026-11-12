// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"crypto/x509"
	"net/url"
	"regexp"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/signkey"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
)

// TektonExecution binds cluster configuration to Environment and Plan identity,
// never logical build identity. API roots and signer keys must be selected by
// the operator independently of any artifact or provider response.
type TektonExecution struct {
	Namespace            string                    `json:"namespace"`
	Platform             string                    `json:"platform"`
	APIServer            string                    `json:"apiServer"`
	CertificateAuthority string                    `json:"certificateAuthority"`
	RunnerImage          string                    `json:"runnerImage"`
	RuntimeImage         string                    `json:"runtimeImage"`
	PublisherPublicKey   string                    `json:"publisherPublicKey"`
	QualifierPublicKey   string                    `json:"qualifierPublicKey"`
	SmokePublicKey       string                    `json:"smokePublicKey"`
	StorageClass         string                    `json:"storageClass"`
	WorkspaceGiB         int                       `json:"workspaceGiB"`
	Smoke                qualificationpolicy.Smoke `json:"smoke"`
}

var tektonNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (environment Environment) validateTekton() error {
	settings := environment.Tekton
	if settings == nil {
		return nil
	}
	if environment.GitHubBundle != "" || environment.GitHubRuntimePlan || environment.GitHubExecutionPartitions ||
		environment.GitHubRuntimeSource != nil || environment.GitHubDevelopment || environment.GitHubARC != "" ||
		len(environment.GitHubDispatchBranches) != 0 || len(environment.GitHubRunners) != 0 || environment.RunsOn != "" ||
		len(environment.ExecutionClasses) != 0 || len(environment.DiskInspectionProfiles) != 0 || environment.DiskEvidence != "" {
		return e.New(e.CodeEngineCapability, "Tekton cannot select GitHub runner, identity or disk policies")
	}
	if !tektonNamePattern.MatchString(settings.Namespace) || !tektonNamePattern.MatchString(settings.StorageClass) ||
		settings.WorkspaceGiB < 1 || settings.WorkspaceGiB > 1024 ||
		(settings.Platform != "linux/amd64" && settings.Platform != "linux/arm64") || settings.Platform != environment.UtilityPlatform {
		return e.New(e.CodeEngineCapability, "Tekton requires an exact namespace, storage class, bounded workspace and one matching native Linux platform")
	}
	endpoint, err := url.Parse(settings.APIServer)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.Path != "" || endpoint.RawPath != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" || endpoint.String() != settings.APIServer {
		return e.New(e.CodeTrustPolicyMissing, "Tekton requires an explicit HTTPS API endpoint without user information, path or query")
	}
	roots := x509.NewCertPool()
	if len(settings.CertificateAuthority) > 64<<10 || !roots.AppendCertsFromPEM([]byte(settings.CertificateAuthority)) {
		return e.New(e.CodeTrustPolicyMissing, "Tekton requires a bounded explicit API certificate authority")
	}
	for _, image := range []string{settings.RunnerImage, settings.RuntimeImage} {
		if !containerPinPattern.MatchString(image) || strings.ContainsAny(image, " \t\r\n\x00$") {
			return e.New(e.CodeUnpinnedSource, "Tekton runner and per-task runtime require exact image digests")
		}
	}
	keys := map[string]bool{}
	for _, key := range []string{settings.PublisherPublicKey, settings.QualifierPublicKey, settings.SmokePublicKey} {
		normalized, err := signkey.NormalizePublicKey([]byte(key))
		if err != nil {
			return err
		}
		if keys[string(normalized)] {
			return e.New(e.CodeTrustPolicyMissing, "Tekton requires distinct explicit public keys for publisher, smoke and qualifier")
		}
		keys[string(normalized)] = true
	}
	if environment.Runtime.Runtime != "docker" || environment.Runtime.RuntimeImage != "" || environment.Runtime.HelperImage != "" ||
		len(environment.Runtime.Services) != 0 || len(environment.Runtime.Setup) != 0 || len(environment.Runtime.Variables) != 0 ||
		len(environment.Runtime.NativeToolchains) != 0 {
		return e.New(e.CodeEngineCapability, "Tekton requires its explicit per-task Docker runtime without ambient setup, services or native toolchains")
	}
	if environment.Publication == nil || environment.Signing == nil || environment.Signing.Mode != "cosign" ||
		environment.Signing.Issuer != settings.APIServer ||
		environment.Signing.Identity != "^"+regexp.QuoteMeta(settings.APIServer+"/api/v1/namespaces/"+settings.Namespace+"/serviceaccounts/nvkr-publisher")+"$" {
		return e.New(e.CodeTrustPolicyMissing, "Tekton requires signed publication under its exact cluster publisher identity")
	}
	if len(environment.Publication.ObjectStores) != 0 || len(environment.Publication.ImageTags) != 0 || environment.GitHubRuntimePlan {
		return e.New(e.CodeEngineCapability, "Tekton does not admit object-store destinations or mutable image aliases")
	}
	if environment.Publication.Mode != "per-output-v1" {
		return e.New(e.CodeEngineCapability, "Tekton requires complete per-output-v1 publication")
	}
	if len(environment.SourceAccess) != 0 || !environment.Publication.Anonymous || environment.Publication.CredentialRef != "" ||
		(environment.ToolchainAccess != nil && (environment.ToolchainAccess.Reader != nil || len(environment.ToolchainAccess.PrivateRoots) != 0)) {
		return e.New(e.CodeEngineCapability, "Tekton currently admits public source/toolchain acquisition and anonymous registry transport; GitHub credential references cannot authorize cluster work")
	}
	if settings.Smoke.Workflow != "" || len(settings.Smoke.Images) != 1 || settings.Smoke.Images[settings.Platform] == "" {
		return e.New(e.CodeEngineCapability, "Tekton smoke requires exactly its native execution image and no hosted workflow identity")
	}
	if err := settings.Smoke.ValidateExecution(); err != nil {
		return err
	}
	return nil
}
