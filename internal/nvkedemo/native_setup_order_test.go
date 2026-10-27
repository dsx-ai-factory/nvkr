// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNativeSharedSetupPrecedesOrdinaryTenantAndStopsDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		ordinary, importFailure bool
	}{
		{"site", false, false}, {"tenant", true, false}, {"policy import failure", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ordinary := tc.ordinary
			root := privateTestDir(t)
			tools := filepath.Join(root, "tools")
			installer := filepath.Join(root, "installer")
			events := filepath.Join(root, "events")
			for _, dir := range []string{tools, filepath.Join(installer, "make"), filepath.Join(installer, "deploy"), filepath.Join(installer, "controller/hack")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			selection := nativeInstallerFixture(t, installer, "dfc817aa34205e8099dad91dfd05170499d00b60", "v1.13.6")
			var err error
			writeTenantFixture(t, filepath.Join(root, "site-prepared.json"), []byte("{\"tpmEkThumbprint\":\""+strings.Repeat("a", 64)+"\"}"), 0600)
			writeTenantFixture(t, filepath.Join(root, "ca.crt"), []byte("fixture certificate"), 0600)
			writeTenantFixture(t, filepath.Join(root, "ca.key"), []byte("fixture key"), 0600)
			cli := filepath.Join(tools, "nvkectl")
			writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
umask 077
case "$1:$2" in
 generate:sitespec)
  case "$*" in *"cluster.podCidr=10.244.0.0/16"*"cluster.serviceCidr=10.96.0.0/16"*) ;; *) exit 54;; esac
  test -f "$NVKECTL_SPIRE_CA_CERT_FILE"
  test -f "$NVKECTL_SPIRE_CA_KEY_FILE"
  case "$*" in *spire.upstreamCA=true*) ;; *) exit 50;; esac
  case "$*" in *spire.caCertFile=*|*spire.caKeyFile=*) exit 51;; esac
  printf 'generate\n' >> "$EVENTS"; printf '{}\n';;
 bootstrap:site)
  test -z "${NVKECTL_BOOTSTRAP_NODE_IMAGE+x}"
  case " $* " in *" --controller-image "*|*" --cwe-image "*) exit 55;; esac
  case " $* " in *" --nvke-controller-image $EXPECTED_CONTROLLER_IMAGE "*) ;; *) exit 56;; esac
  case " $* " in *" --provisioned-provider-image docker.io/library/provider:fixture "*) ;; *) exit 57;; esac
  printf 'bootstrap\n' >> "$EVENTS"
  while test "$#" -gt 0; do
   if test "$1" = --kubeconfig-out; then shift; printf 'private-fixture-config\n' > "$1"; break; fi
   shift
  done;;
 validate:sitespec) printf 'validate\n' >> "$EVENTS";;
 config:set-site) test "$XDG_CONFIG_HOME" = "$FIXTURE/config"; printf 'configure\n' >> "$EVENTS";;
 get:providers) printf 'providers\n' >> "$EVENTS"; printf '[]\n';;
 get:clusters) test "$XDG_CONFIG_HOME" = "$FIXTURE/config"; printf 'tenant-inventory\n' >> "$EVENTS"; exit 7;;
 *) exit 42;;
esac
`), 0700)
			for name, event := range map[string]string{"nerdctl": "nerdctl", "kind": "kind", "clusterctl": "capi", "kubectl": "kubectl"} {
				writeTenantFixture(t, filepath.Join(tools, name), []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' '"+event+"' >> \"$EVENTS\"\n"), 0700)
			}
			for name, event := range map[string]string{"deploy/nvke-deploy.sh": "deploy", "controller/hack/apply-capi-spiffe-exec-auth.sh": "spiffe"} {
				body := "#!/bin/sh\nset -eu\n"
				if event == "deploy" {
					body += ` test -n "$NVKECTL_NICO_TOKEN"
 test -n "$NVKECTL_NODE_USER"
 test -n "$NVKECTL_NODE_PASSWORD"
 test -n "$NVKECTL_DATABASE_PASSWORD"
 test -f "$NVKECTL_SPIRE_CA_CERT_FILE"
 test -f "$NVKECTL_SPIRE_CA_KEY_FILE"
 case "$*" in *--stack-config*) ;; *) exit 53;; esac
 test "$HELM_PLUGINS" = "$FIXTURE/site-helm/plugins"
 test "$NVKR_SITE_KUBECTL" = "$FIXTURE/tools/kubectl"
 test -f "$HELM_PLUGINS/nvkr-site-startup/plugin.yaml"
 case " $* " in *" --timeout "*" -- --post-renderer nvkr-site-startup ") ;; *) exit 58;; esac
`
				}
				body += "printf '%s\\n' '" + event + "' >> \"$EVENTS\"\n"
				writeTenantFixture(t, filepath.Join(installer, name), []byte(body), 0700)
			}
			writeTenantFixture(t, filepath.Join(tools, "kubectl"), []byte(`#!/bin/sh
set -eu
printf 'kubectl\n' >> "$EVENTS"
case "$*" in
 *"get deployments"*) printf '%s\n' '{"items":[{"metadata":{"namespace":"nke-controller"},"spec":{"selector":{"matchLabels":{"app.kubernetes.io/instance":"nke-controller","app.kubernetes.io/name":"cloud-workflow-engine"}}}}]}' ;;
esac
`), 0700)
			handoff := &nativeHandoff{result: make(chan error, 1)}
			handoff.result <- nil
			attempt := nativeSiteAttempt{use: runNativeDemo, bridge: linuxNativeBridge(), bootstrap: &hostBootstrap{}, network: "upstream-ovn", handoff: handoff, files: filepath.Join(root, "files"), assets: NativeAssets{CLI: cli, HostTools: tools, Installer: installer, installerSelection: selection, Images: map[string]NativeImageAsset{
				"controller": {File: "controller.tar", Reference: "docker.io/library/controller:fixture"}, "provisioned-provider": {File: "provider.tar", Reference: "docker.io/library/provider:fixture"}, "profiles": {Reference: "docker.io/library/profiles:fixture"},
			}}}
			tenantFixture := nativeCAPITenantFixture(t, GuestCIDR, "upstream-ovn")
			attempt.boot, attempt.api = tenantFixture.boot, tenantFixture.api
			for _, role := range []string{"ovn", "workload"} {
				attempt.assets.Images[role] = tenantFixture.assets.Images[role]
			}
			installNativeIdentityFixture(t, &attempt, tools, root)
			runner := Runner{Work: root, Env: []string{"PATH=" + tools + ":/usr/bin:/bin", "FIXTURE=" + root, "EVENTS=" + events, "EXPECTED_CONTROLLER_IMAGE=" + attempt.assets.Images["controller"].Reference}}
			var row nativeInstance
			if err = json.Unmarshal([]byte("{\"id\":\"site-fixture\",\"interfaces\":[{\"ipAddresses\":[\"172.30.0.2\",\"172.30.0.3\"]}]}"), &row); err != nil {
				t.Fatal(err)
			}
			server := &OwnedProcess{done: make(chan struct{})}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			runner.Env = SetEnv(runner.Env, map[string]string{"EXPECTED_IDENTITY_DEADLINE": deadline.UTC().Format(time.RFC3339Nano)})
			if tc.importFailure {
				runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_IMPORT_FAILURE": "1"})
			}
			if ordinary {
				err = attempt.ready(ctx, runner, row, tools, root, server)
			} else {
				_, err = attempt.setup(ctx, runner, row, tools, root, server)
			}
			if tc.importFailure {
				var failure *nativeSiteIdentityFailure
				if !errors.As(err, &failure) || CommandExitCode(err) != 17 || strings.Contains(err.Error(), "import-source-policy") {
					t.Fatalf("private import failure crossed its public boundary: %v", err)
				}
				logs, readErr := filepath.Glob(filepath.Join(root, "site-identity-*", "failure.log"))
				if readErr != nil || len(logs) != 1 {
					t.Fatal("missing private identity failure", logs, readErr)
				}
				private, readErr := os.ReadFile(logs[0])
				if readErr != nil || !strings.Contains(string(private), "import-source-policy: command failed (exit 17)") {
					t.Fatalf("underlying private failure not retained: %q, %v", private, readErr)
				}
				raw, readErr := os.ReadFile(events)
				if readErr != nil || strings.Count(string(raw), "identity-import\n") != 1 || strings.Contains(string(raw), "identity-handoff") {
					t.Fatalf("failed policy creation retried or dispatched handoff: %q, %v", raw, readErr)
				}
				return
			}
			if ordinary && CommandExitCode(err) != 7 || !ordinary && err != nil {
				t.Fatalf("unexpected consumer outcome: %v", err)
			}
			raw, readErr := os.ReadFile(events)
			if readErr != nil {
				t.Fatal(readErr)
			}
			got := strings.Split(strings.TrimSpace(string(raw)), "\n")
			want := []string{"generate", "nerdctl", "nerdctl", "kind", "bootstrap", "capi", "kubectl", "validate", "deploy", "spiffe", "identity-stage", "identity-probe", "identity-import", "identity-handoff", "kind", "configure", "providers"}
			// The observed selector and narrow API policy precede the first host API call.
			want = append(want[:15], append([]string{"kubectl", "kubectl"}, want[15:]...)...)
			if ordinary {
				want = append(want, "tenant-inventory")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("shared setup/tenant ordering changed:\ngot=%v\nwant=%v", got, want)
			}
			spec, err := os.ReadFile(filepath.Join(root, "sitespec.yaml"))
			if err != nil || string(spec) != "{}\n" {
				t.Fatalf("current deployment changed the source-generated site spec: %q, %v", spec, err)
			}
			stack, err := os.ReadFile(filepath.Join(root, "stack-config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]json.RawMessage
			if err := json.Unmarshal(stack, &config); err != nil || len(config) != 2 || config["nico"] == nil || config["images"] == nil {
				t.Fatalf("unexpected stack config %s: %v", stack, err)
			}
			if (attempt.tenantOwner != nil) != ordinary {
				t.Fatal("wrong consumer reached tenant ownership")
			}
			_, requestErr := os.Stat(filepath.Join(root, "tenant-request.json"))
			if ordinary && requestErr != nil || !ordinary && !os.IsNotExist(requestErr) {
				t.Fatal("diagnostic and ordinary tenant boundaries differ from contract")
			}
			if _, err = os.Stat(filepath.Join(root, "native-result.json")); !os.IsNotExist(err) {
				t.Fatal("incomplete consumer produced full native result")
			}
			assertNativeIdentityFixtureCompletion(t, root)
			// The test starts no VM, network, Kind or real installer. All external
			// fixture commands above have returned through the actual Runner owner.
		})
	}
}

func TestNativeSiteContractRequiresReviewedSource(t *testing.T) {
	for _, tc := range []struct {
		name, commit string
		valid        bool
	}{
		{"current", "dfc817aa34205e8099dad91dfd05170499d00b60\n", true},
		{"historical", "b6154b292a934dbdff7aa17596cbf2d7ef5f0f85\n", false},
		{"unknown", strings.Repeat("a", 40) + "\n", false},
		{"noncanonical", "dfc817aa34205e8099dad91dfd05170499d00b60", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installer := privateTestDir(t)
			selection := nativeInstallerFixture(t, installer, "dfc817aa34205e8099dad91dfd05170499d00b60", "v1.13.6")
			writeTenantFixture(t, filepath.Join(installer, "SOURCE_COMMIT"), []byte(tc.commit), 0600)
			err := admitNativeSiteContract(t.Context(), NativeAssets{Installer: installer, installerSelection: selection})
			if (err == nil) != tc.valid {
				t.Fatalf("source contract admission: %v", err)
			}
		})
	}
}
