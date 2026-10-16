// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeSharedSetupPrecedesOrdinaryTenantAndStopsDiagnostic(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		t.Run(map[bool]string{false: "site-only diagnostic", true: "ordinary tenant consumer"}[ordinary], func(t *testing.T) {
			root := privateTestDir(t)
			tools := filepath.Join(root, "tools")
			installer := filepath.Join(root, "installer")
			events := filepath.Join(root, "events")
			for _, dir := range []string{tools, filepath.Join(installer, "make"), filepath.Join(installer, "deploy"), filepath.Join(installer, "controller/hack")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			versions, err := os.ReadFile("testdata/native-b615-tool-versions.mk")
			if err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, filepath.Join(installer, "SOURCE_COMMIT"), []byte("b6154b292a934dbdff7aa17596cbf2d7ef5f0f85\n"), 0600)
			writeTenantFixture(t, filepath.Join(installer, "make/tool-versions.mk"), versions, 0600)
			writeTenantFixture(t, filepath.Join(root, "site-prepared.json"), []byte("{\"tpmEkThumbprint\":\""+strings.Repeat("a", 64)+"\"}"), 0600)
			writeTenantFixture(t, filepath.Join(root, "ca.crt"), []byte("fixture certificate"), 0600)
			writeTenantFixture(t, filepath.Join(root, "ca.key"), []byte("fixture key"), 0600)
			cli := filepath.Join(tools, "nvkectl")
			writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
umask 077
case "$1:$2" in
 generate:sitespec) printf 'generate\n' >> "$EVENTS"; printf '{}\n';;
 bootstrap:site)
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
				writeTenantFixture(t, filepath.Join(installer, name), []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' '"+event+"' >> \"$EVENTS\"\n"), 0700)
			}
			handoff := &nativeHandoff{result: make(chan error, 1)}
			handoff.result <- nil
			attempt := nativeSiteAttempt{bridge: linuxNativeBridge(), bootstrap: &hostBootstrap{}, network: "upstream-ovn", handoff: handoff, files: filepath.Join(root, "files"), assets: NativeAssets{CLI: cli, HostTools: tools, Installer: installer, Images: map[string]NativeImageAsset{
				"controller": {File: "controller.tar", Reference: "docker.io/library/controller:fixture"}, "provisioned-provider": {File: "provider.tar", Reference: "docker.io/library/provider:fixture"}, "profiles": {Reference: "docker.io/library/profiles:fixture"},
			}}}
			runner := Runner{Work: root, Env: []string{"PATH=" + tools + ":/usr/bin:/bin", "FIXTURE=" + root, "EVENTS=" + events}}
			var row nativeInstance
			if err = json.Unmarshal([]byte("{\"id\":\"site-fixture\",\"interfaces\":[{\"ipAddresses\":[\"172.30.0.2\",\"172.30.0.3\"]}]}"), &row); err != nil {
				t.Fatal(err)
			}
			server := &OwnedProcess{done: make(chan struct{})}
			if ordinary {
				err = attempt.ready(t.Context(), runner, row, tools, root, server)
			} else {
				_, err = attempt.setup(t.Context(), runner, row, tools, root, server)
			}
			if ordinary && CommandExitCode(err) != 7 || !ordinary && err != nil {
				t.Fatalf("unexpected consumer outcome: %v", err)
			}
			raw, readErr := os.ReadFile(events)
			if readErr != nil {
				t.Fatal(readErr)
			}
			got := strings.Split(strings.TrimSpace(string(raw)), "\n")
			want := []string{"generate", "nerdctl", "nerdctl", "kind", "bootstrap", "capi", "kubectl", "validate", "deploy", "spiffe", "kind", "configure", "providers", "kubectl", "kubectl", "kubectl"}
			if ordinary {
				want = append(want, "tenant-inventory")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("shared setup/tenant ordering changed:\ngot=%v\nwant=%v", got, want)
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
			// The test starts no VM, network, Kind or real installer. All external
			// fixture commands above have returned through the actual Runner owner.
		})
	}
}
