// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"text/template"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"go.yaml.in/yaml/v3"
)

// StartupAsset describes bytes already verified by the dependency or artifact
// owner. Its digest is transport integrity, not an NVKR execution receipt.
type StartupAsset struct {
	Name, URL, Digest string
}

// NativeSiteStartup describes the pre-provisioned site's startup handoff.
// NVKE still owns site-spec generation, bootstrap, rendering and deployment.
// This adapter adds no alternative Kubernetes installer or remote shell.
type NativeSiteStartup struct {
	Name, Gateway, ControllerImage, ProviderImage string
	OVNVersion, OVNImage, KubernetesVersion       string
	Assets                                        []StartupAsset
	HandoffURL, HandoffToken, HandoffCertificate  string
	Deployment                                    *NativeSiteDeployment
	ImageImports                                  map[string]string
}

// NativeSiteDeployment describes the local NICo endpoint and image import aliases.
// NVKE's renderer and deployment executor validate and install the site inputs.
type NativeSiteDeployment struct{ NicoURL, ImageURL, ProfilesImage string }

type startupFile struct {
	Path        string `yaml:"path"`
	Permissions string `yaml:"permissions"`
	Content     string `yaml:"content"`
}

type startupCloudConfig struct {
	WriteFiles []startupFile `yaml:"write_files"`
	RunCmd     [][]string    `yaml:"runcmd"`
	PhoneHome  struct {
		URL   string `yaml:"url"`
		Post  string `yaml:"post"`
		Tries int    `yaml:"tries"`
	} `yaml:"phone_home"`
}

var siteStartupAssetNames = []string{
	"nerdctl", "controller.tar", "profiles.tar", "capnico.tar", "spire-ca.crt", "ovn.tar",
}

func nativeRuntimeImageRoles() []string {
	roles := []string{"workload"}
	for _, role := range runtimeImages {
		roles = append(roles, "runtime-"+role)
	}
	roles = append(roles, nativeCAPIAuthImages...)
	return roles
}

// SiteStartupUserData returns startup configuration without filesystem,
// subprocess or network work. The caller must independently admit the image,
// tools, capacity and owning runtime before using it to create an instance.
// Its work deadline is carried into guest preparation; an unbounded context
// cannot describe when detached guest commands must stop.
func SiteStartupUserData(ctx context.Context, cfg NativeSiteStartup) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeUsage, err, "site startup context")
	}
	if cfg.OVNVersion == "" {
		cfg.OVNVersion = siteOVNVersion
	}
	if err := validateSiteStartup(cfg); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeUsage, err, "site startup configuration")
	}
	importOVN, err := nativeImageImportScript("downloads/ovn.tar", cfg.OVNImage)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeUsage, err, "site OVN import")
	}
	valuesOVN, err := nativeOVNValuesScript(cfg.OVNVersion, cfg.OVNImage)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeUsage, err, "site OVN values")
	}
	assets := slices.Clone(cfg.Assets)
	slices.SortFunc(assets, func(a, b StartupAsset) int { return strings.Compare(a.Name, b.Name) })
	cfg.Assets = assets
	var script bytes.Buffer
	if err := template.Must(template.New("site-startup").Funcs(template.FuncMap{
		"quote":            operatorQuote,
		"configureSpire":   func() string { return siteSpireConfigurationScript },
		"spireEnvironment": func(cfg NativeSiteStartup) string { return string(nativeBootstrapSpireEnvironment(cfg)) },
		"prepareOVN":       func() string { return importOVN + "; " + valuesOVN },
		"sha":              func(value string) string { return strings.TrimPrefix(value, "sha256:") },
		"importImage":      nativeImageImportScript,
		"json":             func(value string) (string, error) { raw, err := json.Marshal(value); return string(raw), err },
	}).Parse(hostSitePreparationScript)).Execute(&script, cfg); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "render site startup handoff")
	}
	preparation, err := guestWorkCommand(ctx, "exec /bin/bash /var/lib/nvke-native/prepare.sh")
	if err != nil {
		return "", err
	}
	config := startupCloudConfig{
		WriteFiles: []startupFile{
			{"/var/lib/nvke-native/prepare.sh", "0700", script.String()},
			{"/etc/systemd/system/nvke-agent.service.d/10-nvkr-console.conf", "0600", "[Service]\nStandardOutput=journal+console\nStandardError=journal+console\n"},
			{"/etc/systemd/system/spire-agent.service.d/10-nvkr-console.conf", "0600", "[Service]\nStandardOutput=journal+console\nStandardError=journal+console\n"},
		},
		RunCmd: [][]string{{"/bin/bash", "-ec", preparation}},
	}
	config.PhoneHome.URL = "http://" + cfg.Gateway + ":7777/latest/meta-data/phone_home"
	config.PhoneHome.Post, config.PhoneHome.Tries = "all", 10
	if cfg.HandoffURL != "" {
		config.WriteFiles = append(config.WriteFiles,
			startupFile{"/var/lib/nvke-native/handoff-ca.pem", "0600", cfg.HandoffCertificate},
			startupFile{"/var/lib/nvke-native/handoff-header", "0600", "Authorization: Bearer " + cfg.HandoffToken + "\n"},
		)
		var status bytes.Buffer
		if err := template.Must(template.New("site-status").Funcs(template.FuncMap{"quote": operatorQuote}).Parse(siteFailureScript)).Execute(&status, cfg); err != nil {
			return "", err
		}
		config.WriteFiles = append(config.WriteFiles, startupFile{"/var/lib/nvke-native/return-status.sh", "0700", status.String()})
	}
	raw, err := yaml.Marshal(config)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "encode site startup cloud-config")
	}
	return "#cloud-config\n" + string(raw), nil
}

func validateSiteStartup(cfg NativeSiteStartup) error {
	if !stableKubernetesVersion(cfg.KubernetesVersion) {
		return fmt.Errorf("require an exact stable Kubernetes version selected by NVKE")
	}
	if cfg.OVNVersion != siteOVNVersion && cfg.OVNVersion != upstreamSiteOVNVersion {
		return fmt.Errorf("require the explicit supported OVN version")
	}
	if !safePhase(cfg.Name) || len(cfg.Name) > 63 || strings.Contains(cfg.Name, "_") {
		return fmt.Errorf("require a canonical site name")
	}
	gateway, err := netip.ParseAddr(cfg.Gateway)
	if err != nil || !gateway.Is4() || !gateway.IsPrivate() {
		return fmt.Errorf("require the declared private IPv4 vmnet gateway")
	}
	for _, image := range []string{cfg.ControllerImage, cfg.ProviderImage} {
		if image == "" || len(image) > 4096 || strings.ContainsAny(image, "\r\n\x00") {
			return fmt.Errorf("require both selected image references")
		}
		if _, err := name.ParseReference(image, name.StrictValidation); err != nil {
			return fmt.Errorf("invalid selected image reference: %w", err)
		}
	}
	expected := slices.Clone(siteStartupAssetNames)
	roles := []string{"controller", "profiles", "capnico"}
	if len(cfg.ImageImports) > len(roles) {
		for _, role := range nativeRuntimeImageRoles() {
			roles = append(roles, role)
			expected = append(expected, role+".tar")
		}
	}
	if len(cfg.ImageImports) != len(roles) {
		return errors.New("site startup requires every selected image reference")
	}
	for _, role := range roles {
		reference := cfg.ImageImports[role]
		if _, err := nativeImageImportScript("downloads/"+role+".tar", reference); err != nil {
			return fmt.Errorf("site image %s: %w", role, err)
		}
		if strings.HasPrefix(role, "runtime-") || strings.HasPrefix(role, "capi-exec-") {
			if !strings.HasPrefix(reference, "docker.io/nvkr-local/"+strings.TrimPrefix(role, "runtime-")+"@") {
				return fmt.Errorf("site runtime image %s differs from its selected local alias", role)
			}
		}
	}

	if cfg.Deployment != nil {
		for _, location := range []string{cfg.Deployment.NicoURL, cfg.Deployment.ImageURL} {
			u, err := url.Parse(location)
			if err != nil || u.Scheme != "http" || u.Hostname() != cfg.Gateway || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(location) > 4096 {
				return fmt.Errorf("deployment endpoints must use the declared private gateway")
			}
		}
		if _, err := name.ParseReference(cfg.Deployment.ProfilesImage, name.StrictValidation); err != nil {
			return fmt.Errorf("invalid selected profiles image: %w", err)
		}
	}
	slices.Sort(expected)
	if len(cfg.Assets) != len(expected) {
		return fmt.Errorf("site startup requires the exact bounded asset set")
	}
	var names []string
	for _, asset := range cfg.Assets {
		if len(asset.Name) > 64 || len(asset.URL) > 4096 {
			return fmt.Errorf("site startup asset description exceeds its bound")
		}
		names = append(names, asset.Name)
		if !canonical.IsDigest(asset.Digest) {
			return fmt.Errorf("asset %q lacks exact-byte integrity", asset.Name)
		}
		location, err := url.Parse(asset.URL)
		if err != nil || location.Scheme != "http" || location.Hostname() != cfg.Gateway ||
			location.Port() == "" || location.User != nil || location.RawQuery != "" || location.Fragment != "" ||
			!strings.HasPrefix(location.Path, "/files/") || strings.ContainsAny(asset.URL, "\r\n\x00") {
			return fmt.Errorf("asset %q must use the owning NICo file service at the declared gateway", asset.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, expected) {
		return fmt.Errorf("site startup requires each selected binary, installer and bootstrap image exactly once")
	}
	return validateStartupHandoff(cfg)
}

func validateStartupHandoff(cfg NativeSiteStartup) error {
	if cfg.HandoffURL == "" && cfg.HandoffToken == "" && cfg.HandoffCertificate == "" {
		return nil
	}
	if len(cfg.HandoffURL) > 4096 || len(cfg.HandoffToken) != 64 || len(cfg.HandoffCertificate) > 16<<10 {
		return fmt.Errorf("site handoff description exceeds its bound or lacks its issued token")
	}
	location, err := url.Parse(cfg.HandoffURL)
	if err != nil || location.Scheme != "https" || location.Hostname() != cfg.Gateway || location.Port() == "" ||
		location.Path != "/site-kubeconfig" || location.User != nil || location.RawQuery != "" || location.Fragment != "" ||
		strings.ContainsAny(cfg.HandoffURL, "\r\n\x00") {
		return fmt.Errorf("site handoff must use its private HTTPS return channel")
	}
	token, err := hex.DecodeString(cfg.HandoffToken)
	if err != nil || len(token) != 32 {
		return fmt.Errorf("site handoff requires its issued 256-bit token")
	}
	block, rest := pem.Decode([]byte(cfg.HandoffCertificate))
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return fmt.Errorf("site handoff requires its issued TLS certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("site handoff certificate: %w", err)
	}
	return certificate.VerifyHostname(cfg.Gateway)
}

// The explicitly selected weekend SPIRE package uses the earlier NKE unit
// name. Both packages retain TPM attestation and the same SPIRE configuration
// paths. Admit exactly one installed package unit rather than creating aliases
// or changing either source package.
const siteSpireConfigurationScript = `spire_configure_unit=
for candidate in nvke-spire-configure.service nke-spire-configure.service; do
  if [[ -f "/lib/systemd/system/$candidate" ]]; then
    if [[ -n "$spire_configure_unit" ]]; then
      printf 'Ambiguous installed SPIRE configuration units\n' >&2
      exit 1
    fi
    spire_configure_unit=$candidate
  fi
done
if [[ -z "$spire_configure_unit" ]]; then
  printf 'Required SPIRE configuration unit is missing\n' >&2
  exit 1
fi
systemctl restart "$spire_configure_unit"`

const siteFailureScript = `#!/bin/bash
report_failure() {
  result=$?
  if [[ "$result" != 0 ]]; then
    report="$work/bootstrap.log"
    [[ -f "$report" ]] || report=/var/log/cloud-init-output.log
    head -c 1048576 "$report" > "$work/startup-error.log"
    curl --fail --silent --show-error --connect-timeout 5 --max-time 15 \
      --proto '=https' --cacert "$work/handoff-ca.pem" --header @"$work/handoff-header" \
      --header 'X-NVKE-Startup-Status: failed' --data-binary @"$work/startup-error.log" {{quote .HandoffURL}} || true
  fi
}
trap report_failure EXIT
`
