// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const operatorServiceFixture = `{"items":[{"metadata":{"name":"observed-ui","namespace":"temporal","uid":"service-uid","resourceVersion":"123","labels":{"app.kubernetes.io/instance":"temporal","app.kubernetes.io/name":"temporal","app.kubernetes.io/component":"web"}},"spec":{"selector":{"app.kubernetes.io/instance":"temporal","app.kubernetes.io/name":"temporal","app.kubernetes.io/component":"web"},"ports":[{"port":8123,"protocol":"TCP"}]}}]}`

func operatorSessionFixture(t *testing.T) ManagementSession {
	t.Helper()
	work := filepath.Join(t.TempDir(), "operator's private session with spaces")
	tools := filepath.Join(work, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kubeconfig", "tenant-kubeconfig"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("secret-config-sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
set -eu
case "$*" in
  *--raw=/readyz*) printf '%s\n' "${READY_REPLY:-ok}" ;;
  *--raw=/api/v1/namespaces/temporal/services/observed-ui:8123/proxy/*) test "${UI_FAIL:-0}" = 0; printf '%s' "${UI_HTTP-<html>private-ui-response-sentinel</html>}" ;;
  *--raw=/api/v1/namespaces/temporal/services/*/proxy/*) exit 1 ;;
  *'get services -o json'*)
    if test -f "$0.readback"; then printf '%s\n' "${UI_READBACK-$UI_JSON}";
    else touch "$0.readback"; printf '%s\n' "$UI_JSON"; fi ;;
  *) printf 'access-command-executed\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(tools, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return ManagementSession{Work: work, Tools: tools, Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "UI_JSON=" + operatorServiceFixture}}}
}

func TestOperatorAccessInspectsAPIsBeforeShellInstructions(t *testing.T) {
	for _, test := range []struct {
		name, reply, ui string
		bad             bool
	}{
		{"discovered service", "ok", operatorServiceFixture, false},
		{"no service", "ok", `{"items":[]}`, false},
		{"invalid optional response", "ok", "invalid JSON", false},
		{"API not ready", "not ready", operatorServiceFixture, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := operatorSessionFixture(t)
			session.Runner.Env = SetEnv(session.Runner.Env, map[string]string{"READY_REPLY": test.reply, "UI_JSON": test.ui})
			access, err := InspectOperatorAccess(t.Context(), session)
			if test.bad {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || access != (OperatorAccess{}) {
					t.Fatalf("unready API produced access: %#v %v", access, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err = access.WriteInstructions(&output); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			for _, forbidden := range []string{"secret-config-sentinel", "private-ui-response-sentinel", "Services left running", "cleanup returned successfully", "…"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("access projection leaked or overclaimed %q", forbidden)
				}
			}
			if test.ui == operatorServiceFixture {
				if !strings.Contains(text, "'service/observed-ui' 8080:8123") || !strings.Contains(text, "no forward is listening yet") || !strings.Contains(text, "--address 127.0.0.1") {
					t.Fatalf("missing observed target or listener distinction: %s", text)
				}
			} else if strings.Contains(text, "http://") || !strings.Contains(text, "no unambiguous Temporal UI") {
				t.Fatalf("invented browser endpoint: %s", text)
			}
			// Execute the generated assignments and exact read-only commands with
			// a real shell. Spaces/apostrophes must survive without evaluation.
			var commands []string
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "KUBECTL=") || strings.HasPrefix(line, "SITE_CONFIG=") || strings.HasPrefix(line, "TENANT_CONFIG=") || strings.HasPrefix(line, `"$KUBECTL"`) && !strings.Contains(line, "port-forward") {
					commands = append(commands, line)
				}
			}
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-eu", "-c", strings.Join(commands, "\n"))
			if result, err := cmd.CombinedOutput(); err != nil || strings.Count(string(result), "access-command-executed") < 3 {
				t.Fatalf("copyable commands failed: %s %v", result, err)
			}
		})
	}
}

func TestOperatorAccessRejectsUnsafeOrMissingSessionFiles(t *testing.T) {
	for _, mode := range []string{"missing", "symlink", "public config", "not executable", "controls"} {
		t.Run(mode, func(t *testing.T) {
			session := operatorSessionFixture(t)
			config := filepath.Join(session.Work, "tenant-kubeconfig")
			var err error
			switch mode {
			case "missing", "symlink":
				err = os.Remove(config)
				if err == nil && mode == "symlink" {
					err = os.Symlink(filepath.Join(session.Work, "kubeconfig"), config)
				}
			case "public config":
				err = os.Chmod(config, 0o644)
			case "not executable":
				err = os.Chmod(filepath.Join(session.Tools, "kubectl"), 0o600)
			case "controls":
				session.Work += "\n"
			}
			if err != nil {
				t.Fatal(err)
			}
			access, err := InspectOperatorAccess(t.Context(), session)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || access != (OperatorAccess{}) {
				t.Fatalf("unsafe session admitted: %v %#v", err, access)
			}
		})
	}
}

func TestOperatorUIServiceDoesNotGuessAmbiguousPorts(t *testing.T) {
	for _, mutation := range []string{"none", "duplicate", "extra port", "UDP", "wrong namespace", "wrong labels", "invalid port", "missing UID", "missing version", "wrong selector", "empty selector"} {
		t.Run(mutation, func(t *testing.T) {
			var services operatorServices
			if err := json.Unmarshal([]byte(operatorServiceFixture), &services); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "duplicate":
				services.Items = append(services.Items, services.Items[0])
			case "extra port":
				services.Items[0].Spec.Ports = append(services.Items[0].Spec.Ports, services.Items[0].Spec.Ports[0])
			case "UDP":
				services.Items[0].Spec.Ports[0].Protocol = "UDP"
			case "wrong namespace":
				services.Items[0].Metadata.Namespace = "other"
			case "wrong labels":
				services.Items[0].Metadata.Labels["app.kubernetes.io/component"] = "frontend"
			case "invalid port":
				services.Items[0].Spec.Ports[0].Port = 0
			case "missing UID":
				services.Items[0].Metadata.UID = ""
			case "missing version":
				services.Items[0].Metadata.ResourceVersion = ""
			case "wrong selector":
				services.Items[0].Spec.Selector["app.kubernetes.io/component"] = "frontend"
			case "empty selector":
				services.Items[0].Spec.Selector = nil
			}
			name, port := operatorUIService(services)
			if mutation == "none" {
				if name != "observed-ui" || port != 8123 {
					t.Fatalf("lost exact service port: %s %d", name, port)
				}
			} else if name != "" || port != 0 {
				t.Fatalf("ambiguous UI yielded a forward: %s %d", name, port)
			}
		})
	}
}

func TestOperatorUIHTTPRequiresResponseAndUnchangedService(t *testing.T) {
	for _, mutation := range []string{"HTTP failure", "empty HTTP", "replaced UID", "changed version", "changed port", "changed selector", "missing service", "invalid readback", "same service"} {
		t.Run(mutation, func(t *testing.T) {
			session := operatorSessionFixture(t)
			var readback operatorServices
			if err := json.Unmarshal([]byte(operatorServiceFixture), &readback); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			switch mutation {
			case "HTTP failure":
				env["UI_FAIL"] = "1"
			case "empty HTTP":
				env["UI_HTTP"] = ""
			case "replaced UID":
				readback.Items[0].Metadata.UID = "replacement"
			case "changed version":
				readback.Items[0].Metadata.ResourceVersion = "124"
			case "changed port":
				readback.Items[0].Spec.Ports[0].Port++
			case "changed selector":
				readback.Items[0].Spec.Selector["extra-selector"] = "replacement"
			case "missing service":
				readback.Items = nil
			case "invalid readback":
				env["UI_READBACK"] = "invalid JSON"
			}
			if _, exists := env["UI_READBACK"]; !exists {
				raw, err := json.Marshal(readback)
				if err != nil {
					t.Fatal(err)
				}
				env["UI_READBACK"] = string(raw)
			}
			session.Runner.Env = SetEnv(session.Runner.Env, env)
			access, err := InspectOperatorAccess(t.Context(), session)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := access.WriteInstructions(&output); err != nil {
				t.Fatal(err)
			}
			verified := mutation == "same service"
			if access.uiHTTPObserved != verified || strings.Contains(output.String(), "http://127.0.0.1:8080") != verified {
				t.Fatalf("browser guidance not bound to observed HTTP/Service: %s", &output)
			}
			if !strings.Contains(output.String(), "get nodes") || strings.Contains(output.String(), "private-ui-response-sentinel") {
				t.Fatal("optional UI failure removed Kubernetes access or exposed response")
			}
		})
	}
}

type operatorShortWriter struct{}

func (operatorShortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestOperatorAccessContextAndOutputFailuresAreCoded(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code nvkrerr.Code
	}{
		{"nil context", nil, nvkrerr.CodeUsage},
		{"canceled context", canceled, nvkrerr.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			access, err := InspectOperatorAccess(test.ctx, ManagementSession{})
			var coded *nvkrerr.Error
			if access != (OperatorAccess{}) || !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("context lost classification: %#v %v", access, err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		access OperatorAccess
		writer io.Writer
		code   nvkrerr.Code
	}{
		{"uninspected access", OperatorAccess{}, io.Discard, nvkrerr.CodeUsage},
		{"nil writer", OperatorAccess{kubectl: "/tool", site: "/site", tenant: "/tenant"}, nil, nvkrerr.CodeUsage},
		{"short write", OperatorAccess{kubectl: "/tool", site: "/site", tenant: "/tenant"}, operatorShortWriter{}, nvkrerr.CodePreflightFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.access.WriteInstructions(test.writer)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("output failure lost classification: %v", err)
			}
		})
	}
}
