// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const registryImage = "docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8"

var runtimeImages = []string{"csi-node-driver-registrar", "postgresql", "spiffe-csi", "spire-agent", "spire-bash", "spire-controller-manager", "spire-oidc", "spire-server", "spire-tpm-server", "temporal-admin-tools", "temporal-server", "temporal-ui"}

// SelectedOutput is an observed immutable attempt description. nvkr and ORAS,
// not this struct, verify byte integrity at extraction and registry handoff.
type SelectedOutput struct {
	Layout      string
	Observation lifecycle.Artifact
	imageTag    string
}
type kindNetwork struct{ Gateway, CIDR, ID string }

// The build and deployment preflights inspect the same native Docker network;
// neither assumes a default subnet or accepts a caller-supplied gateway.
func admitKindNetwork(ctx context.Context, runner Runner) (kindNetwork, error) {
	type networkDescription struct {
		ID, Driver string
		IPAM       struct {
			Config []struct{ Gateway, Subnet string }
		}
	}
	networks, err := captureJSON[[]networkDescription](ctx, runner, []string{"docker", "network", "inspect", "kind"}, "admit-kind-network")
	if err != nil {
		return kindNetwork{}, err
	}
	if len(networks) != 1 || networks[0].Driver != "bridge" || networks[0].ID == "" {
		return kindNetwork{}, errors.New("require existing Kind bridge network")
	}
	gateway, cidr := "", ""
	for _, config := range networks[0].IPAM.Config {
		address, readErr := netip.ParseAddr(config.Gateway)
		if readErr != nil {
			return kindNetwork{}, errors.New("invalid Kind gateway")
		}
		if !address.Is4() {
			continue
		}
		if gateway != "" || !address.IsPrivate() {
			return kindNetwork{}, errors.New("ambiguous private Kind IPv4 gateway")
		}
		prefix, readErr := netip.ParsePrefix(config.Subnet)
		if readErr != nil || !prefix.Contains(address) {
			return kindNetwork{}, errors.New("invalid Kind subnet")
		}
		gateway, cidr = address.String(), prefix.String()
	}
	if gateway == "" {
		return kindNetwork{}, errors.New("missing private Kind IPv4 gateway")
	}
	if err = validateNetworkKind(cidr); err != nil {
		return kindNetwork{}, err
	}
	if err = admitSiteCIDRs(cidr); err != nil {
		return kindNetwork{}, err
	}
	return kindNetwork{Gateway: gateway, CIDR: cidr, ID: networks[0].ID}, nil
}

// This admission is a point-in-time availability check, not port ownership or
// cleanup authorization. The owning simulator still has to bind at launch.
const simulatorAPIPort = 18089

func localJSONRequest(ctx context.Context, address string, timeout time.Duration, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("local service did not return200")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return err
	}
	if len(raw) > 4096 {
		return errors.New("local response exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.New("trailing local service response")
	}
	return nil
}

type pinnedImage struct {
	Registry   string `json:"registry,omitempty"`
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
}
type imageOverride struct {
	Image pinnedImage `json:"image"`
}
type spireServerOverride struct {
	Image             pinnedImage   `json:"image"`
	ControllerManager imageOverride `json:"controllerManager"`
	NodeAttestor      struct {
		TPMDirect imageOverride `json:"tpmDirect"`
	} `json:"nodeAttestor"`
	Tests struct {
		Bash imageOverride `json:"bash"`
	} `json:"tests"`
}
type spireAgentOverride struct {
	Image           pinnedImage   `json:"image"`
	SocketAlternate imageOverride `json:"socketAlternate"`
	FSGroupFix      imageOverride `json:"fsGroupFix"`
}
type spireCSIOverride struct {
	Image               pinnedImage   `json:"image"`
	NodeDriverRegistrar imageOverride `json:"nodeDriverRegistrar"`
}
type spireOIDCOverride struct {
	Image pinnedImage `json:"image"`
	Tests struct {
		Bash imageOverride `json:"bash"`
	} `json:"tests"`
}
type spireOverrides struct {
	Server spireServerOverride `json:"spire-server"`
	Agent  spireAgentOverride  `json:"spire-agent"`
	CSI    spireCSIOverride    `json:"spiffe-csi-driver"`
	OIDC   spireOIDCOverride   `json:"spiffe-oidc-discovery-provider"`
}
type postgresOverrides struct {
	Global struct {
		Security struct {
			AllowInsecureImages bool `json:"allowInsecureImages"`
		} `json:"security"`
	} `json:"global"`
	Image pinnedImage `json:"image"`
}
type temporalOverrides struct {
	Server     imageOverride `json:"server"`
	AdminTools imageOverride `json:"admintools"`
	Web        imageOverride `json:"web"`
}

func runtimeValues(host string, outputs map[string]SelectedOutput) map[string]any {
	image := func(key string) pinnedImage {
		return pinnedImage{Registry: host, Repository: "nvkr-local/" + key, Tag: outputs["runtime-"+key].Observation.Digest}
	}
	var spire spireOverrides
	spire.Server.Image = image("spire-server")
	spire.Server.ControllerManager.Image = image("spire-controller-manager")
	spire.Server.NodeAttestor.TPMDirect.Image = image("spire-tpm-server")
	spire.Server.Tests.Bash.Image = image("spire-bash")
	spire.Agent.Image = image("spire-agent")
	spire.Agent.SocketAlternate.Image = image("spire-bash")
	spire.Agent.FSGroupFix.Image = image("spire-bash")
	spire.CSI.Image = image("spiffe-csi")
	spire.CSI.NodeDriverRegistrar.Image = image("csi-node-driver-registrar")
	spire.OIDC.Image = image("spire-oidc")
	spire.OIDC.Tests.Bash.Image = image("spire-bash")
	var postgres postgresOverrides
	postgres.Global.Security.AllowInsecureImages = true
	postgres.Image = pinnedImage{Registry: host, Repository: "nvkr-local/postgresql", Digest: outputs["runtime-postgresql"].Observation.Digest}
	temporalImage := func(key string) imageOverride {
		return imageOverride{Image: pinnedImage{Repository: host + "/nvkr-local/" + key, Tag: outputs["runtime-"+key].imageTag + "@" + outputs["runtime-"+key].Observation.Digest}}
	}
	return map[string]any{"spire": spire, "postgresql": postgres, "temporal": temporalOverrides{Server: temporalImage("temporal-server"), AdminTools: temporalImage("temporal-admin-tools"), Web: temporalImage("temporal-ui")}}
}

type podMetadata struct {
	UID, Namespace, Name string
	Labels               map[string]string
}
type profileMount struct {
	Name, MountPath string
	ReadOnly        bool
}
type profileVolume struct {
	Name     string
	HostPath *struct{ Path, Type string }
	EmptyDir *json.RawMessage
}
type podContainer struct {
	Name, Image  string
	VolumeMounts []profileMount
}
type podContainerState struct {
	Name, ImageID string
	Ready         bool
	State         struct {
		Running    *json.RawMessage
		Terminated *struct{ ExitCode *int }
	}
}

func bootstrapCA(ctx context.Context, session ManagementSession) error {
	cert, key := filepath.Join(session.Work, "ca.crt"), filepath.Join(session.Work, "ca.key")
	// Bootstrap and retained sites share this finite root. Match NVKE's local
	// E2E CA lifetime so retaining a demo does not expire its root after one day.
	if err := session.Runner.Run(ctx, []string{"openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "3650", "-subj", "/CN=nvkr-local-demo", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-keyout", key, "-out", cert}, "local-bootstrap-ca", 30*time.Second); err != nil {
		return err
	}
	if err := os.Chmod(key, 0o600); err != nil {
		return err
	}
	return session.Runner.Run(ctx, []string{"openssl", "verify", "-x509_strict", "-CAfile", cert, cert}, "check-local-bootstrap-ca", 30*time.Second)
}

func randomScalar() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
