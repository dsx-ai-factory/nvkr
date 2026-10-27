// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// OperatorAccess is a read-only access projection, not a readiness receipt or
// permission to retain, signal, or delete resources. Only InspectOperatorAccess
// supplies its locators. A supervisor must separately acknowledge retention
// before any presenter describes the environment as left running.
type OperatorAccess struct {
	kubectl, site, tenant string
	siteQCOW              bool
	uiService             string
	uiPort                int
	uiHTTPObserved        bool
}

// InspectOperatorAccess checks the actual session's tools and kubeconfigs and
// queries both APIs. It never reads kubeconfig contents into presentation or
// changes the caller's ambient KUBECONFIG. Optional UI discovery is descriptive:
// it does not open a listener or prove a browser endpoint is reachable. HTTP
// guidance additionally requires an actual bounded Service-proxy response.
func InspectOperatorAccess(ctx context.Context, session ManagementSession) (OperatorAccess, error) {
	access, err := inspectOperatorAccess(ctx, session)
	if err != nil {
		code := nvkrerr.CodePreflightFailed
		if ctx == nil {
			code = nvkrerr.CodeUsage
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = nvkrerr.CodeTimeout
		}
		return OperatorAccess{}, nvkrerr.Wrap(code, err, "inspect local demo access")
	}
	return access, nil
}

func inspectOperatorAccess(ctx context.Context, session ManagementSession) (OperatorAccess, error) {
	if err := requireContext(ctx); err != nil {
		return OperatorAccess{}, err
	}
	access := OperatorAccess{kubectl: filepath.Join(session.Tools, "kubectl"), site: filepath.Join(session.Work, "kubeconfig"), tenant: filepath.Join(session.Work, "tenant-kubeconfig")}
	if err := access.inspectFiles(); err != nil {
		return OperatorAccess{}, err
	}
	return inspectOperatorAPIs(ctx, session, access)
}

func (access OperatorAccess) inspectFiles() error {
	for _, path := range []string{access.kubectl, access.site, access.tenant} {
		if !operatorArgument(path) || !filepath.IsAbs(path) {
			return errors.New("access requires absolute printable session locators")
		}
		held, info, err := securepath.OpenRegular(path)
		if err != nil {
			return errors.New("session access tool or kubeconfig is unavailable")
		}
		closeErr := held.Close()
		if closeErr != nil || !currentOwner(info) || info.Size() <= 0 || path == access.kubectl && info.Mode().Perm()&0o100 == 0 || path != access.kubectl && info.Mode().Perm()&0o077 != 0 {
			return errors.New("session access files must be private, owned and usable")
		}
	}
	return nil
}

func inspectOperatorAPIs(ctx context.Context, session ManagementSession, access OperatorAccess) (OperatorAccess, error) {
	for index, config := range []string{access.site, access.tenant} {
		raw, err := session.Runner.Capture(ctx, []string{access.kubectl, "--kubeconfig", config, "--request-timeout=10s", "get", "--raw=/readyz"}, fmt.Sprintf("operator-api-%d", index), 15*time.Second)
		if err != nil {
			return OperatorAccess{}, err
		}
		if strings.TrimSpace(string(raw)) != "ok" {
			return OperatorAccess{}, errors.New("session Kubernetes API did not report ready")
		}
	}
	// A named UI Service/port comes only from this API response. Absence,
	// ambiguity or optional discovery failure must never invent a browser URL.
	services, err := captureJSON[operatorServices](ctx, session.Runner, []string{access.kubectl, "--kubeconfig", access.site, "--request-timeout=10s", "-n", "temporal", "get", "services", "-o", "json"}, "operator-ui-services")
	if errors.Is(err, ErrUnjoined) || ctx.Err() != nil {
		return OperatorAccess{}, errors.Join(err, ctx.Err())
	}
	if err == nil {
		access.uiService, access.uiPort = operatorUIService(services)
		if access.uiService != "" {
			// Kubernetes owns Service routing and HTTP error handling. A TCP port
			// declaration alone does not establish an HTTP endpoint. Do not
			// interpret or print the private response body, or claim UI health.
			proxy := "/api/v1/namespaces/temporal/services/" + url.PathEscape(fmt.Sprintf("%s:%d", access.uiService, access.uiPort)) + "/proxy/"
			response, probeErr := session.Runner.Capture(ctx, []string{access.kubectl, "--kubeconfig", access.site, "--request-timeout=10s", "get", "--raw=" + proxy}, "operator-ui-http", 15*time.Second)
			if errors.Is(probeErr, ErrUnjoined) || ctx.Err() != nil {
				return OperatorAccess{}, errors.Join(probeErr, ctx.Err())
			}
			if probeErr == nil && len(response) != 0 {
				readback, readErr := captureJSON[operatorServices](ctx, session.Runner, []string{access.kubectl, "--kubeconfig", access.site, "--request-timeout=10s", "-n", "temporal", "get", "services", "-o", "json"}, "operator-ui-readback")
				if errors.Is(readErr, ErrUnjoined) || ctx.Err() != nil {
					return OperatorAccess{}, errors.Join(readErr, ctx.Err())
				}
				// Bind the response to the same selected Service UID/version and
				// port description. API observations are not artifact receipts.
				access.uiHTTPObserved = readErr == nil && operatorSameUI(services, readback)
			}
		}
	}
	return access, nil
}

type operatorServices struct {
	Items []struct {
		Metadata struct {
			Name, Namespace, UID, ResourceVersion string
			Labels                                map[string]string
		}
		Spec struct {
			Selector map[string]string
			Ports    []struct {
				Port     int
				Protocol string
			}
		}
	}
}

func operatorUIService(services operatorServices) (string, int) {
	name, port := "", 0
	for _, service := range services.Items {
		labels := service.Metadata.Labels
		if service.Metadata.Namespace != "temporal" || labels["app.kubernetes.io/instance"] != "temporal" || labels["app.kubernetes.io/name"] != "temporal" || labels["app.kubernetes.io/component"] != "web" {
			continue
		}
		if name != "" || !operatorArgument(service.Metadata.Name) || service.Metadata.UID == "" || service.Metadata.ResourceVersion == "" || len(service.Spec.Ports) != 1 {
			return "", 0
		}
		selector := service.Spec.Selector
		if selector["app.kubernetes.io/instance"] != "temporal" || selector["app.kubernetes.io/name"] != "temporal" || selector["app.kubernetes.io/component"] != "web" {
			return "", 0
		}
		selected := service.Spec.Ports[0]
		if selected.Port < 1 || selected.Port > 65535 || selected.Protocol != "TCP" {
			return "", 0
		}
		name, port = service.Metadata.Name, selected.Port
	}
	return name, port
}

func operatorSameUI(before, after operatorServices) bool {
	name, port := operatorUIService(before)
	afterName, afterPort := operatorUIService(after)
	if name == "" || name != afterName || port != afterPort {
		return false
	}
	for _, first := range before.Items {
		if first.Metadata.Name != name || first.Metadata.Namespace != "temporal" {
			continue
		}
		for _, second := range after.Items {
			if second.Metadata.Name == name && second.Metadata.Namespace == "temporal" {
				return reflect.DeepEqual(first, second)
			}
		}
	}
	return false
}

func operatorArgument(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

// Use the existing repository single-quote pattern, not a shell parser. The
// caller has already rejected controls; quoting is separate from that check.
func operatorQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// WriteInstructions emits complete, untruncated POSIX-shell commands. It must
// run after terminal restoration, not through an evictable activity ring.
// These instructions are useful only while the supervisor retains the session.
func (access OperatorAccess) WriteInstructions(out io.Writer) error {
	if out == nil || access.kubectl == "" || access.site == "" || access.tenant == "" {
		return nvkrerr.New(nvkrerr.CodeUsage, "operator instructions require inspected access and an output writer")
	}
	var text strings.Builder
	fmt.Fprintf(&text, "\nKubernetes access (on this execution host):\nKUBECTL=%s\nSITE_CONFIG=%s\nTENANT_CONFIG=%s\n\n", operatorQuote(access.kubectl), operatorQuote(access.site), operatorQuote(access.tenant))
	text.WriteString("# Tenant: the real QCOW2 nodes and their workloads\n\"$KUBECTL\" --kubeconfig \"$TENANT_CONFIG\" --request-timeout=10s get nodes\n\"$KUBECTL\" --kubeconfig \"$TENANT_CONFIG\" --request-timeout=10s get pods -A\n")
	if access.siteQCOW {
		text.WriteString("\n# Management: native QCOW2 site, provisioning and supporting services\n")
	} else {
		text.WriteString("\n# Management: Kind nodes, provisioning and supporting services\n")
	}
	text.WriteString("\"$KUBECTL\" --kubeconfig \"$SITE_CONFIG\" --request-timeout=10s get nodes\n\"$KUBECTL\" --kubeconfig \"$SITE_CONFIG\" --request-timeout=10s get pods -A\n")
	if access.uiService == "" {
		text.WriteString("\nBrowser access: no unambiguous Temporal UI Service/port was discovered.\n\"$KUBECTL\" --kubeconfig \"$SITE_CONFIG\" --request-timeout=10s -n temporal get services\n")
	} else if !access.uiHTTPObserved {
		text.WriteString("\nTemporal UI Service discovered, but its HTTP response was not verified. No browser URL is promised.\n\"$KUBECTL\" --kubeconfig \"$SITE_CONFIG\" --request-timeout=10s -n temporal get services,pods\n")
	} else {
		fmt.Fprintf(&text, "\nTemporal UI: HTTP response observed through the Kubernetes Service proxy; no forward is listening yet.\nThis is an access check, not functional UI health.\nStart a loopback-only forward on this execution host:\n\"$KUBECTL\" --kubeconfig \"$SITE_CONFIG\" -n temporal port-forward --address 127.0.0.1 %s 8080:%d\nThen open http://127.0.0.1:8080 on that host. Stop the forward with Ctrl+C; this does not delete the demo.\nFor a browser on another computer, use your existing SSH tunnel to this host; no remote address is assumed.\n", operatorQuote("service/"+access.uiService), access.uiPort)
	}
	return writeOperatorText(out, text.String())
}
