package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestImagePinResolutionSelectsRequestedLinuxManifests(t *testing.T) {
	amd := "sha256:" + strings.Repeat("a", 64)
	arm := "sha256:" + strings.Repeat("b", 64)
	manifest := `{"manifests":[{"digest":"` + amd + `","platform":{"os":"linux","architecture":"amd64"}},{"digest":"` + arm + `","platform":{"os":"linux","architecture":"arm64"}},{"digest":"sha256:` + strings.Repeat("c", 64) + `","platform":{"os":"windows","architecture":"amd64"}}]}`
	for _, tc := range []struct {
		name, response string
		arches         []string
		want           map[string]string
		code           e.Code
		fail, canceled bool
	}{
		{name: "both architectures", response: manifest, arches: []string{"arm64", "amd64"}, want: map[string]string{"amd64": amd, "arm64": arm}},
		{name: "subset", response: manifest, arches: []string{"arm64"}, want: map[string]string{"arm64": arm}},
		{name: "missing requested architecture", response: manifest, arches: []string{"amd64", "s390x"}, code: e.CodeFetchFailed},
		{name: "single manifest lacks platform list", response: `{"schemaVersion":2}`, arches: []string{"amd64"}, code: e.CodeFetchFailed},
		{name: "malformed response", response: `{`, arches: []string{"amd64"}, code: e.CodeFetchFailed},
		{name: "process failure", fail: true, code: e.CodeFetchFailed},
		{name: "canceled before process", canceled: true, code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "docker")
			// This executable models only Docker's manifest-inspect protocol; it
			// does not assert successful registry authentication or publication.
			script := "#!/bin/sh\n[ \"$#\" = 3 ] && [ \"$1\" = manifest ] && [ \"$2\" = inspect ] && [ \"$3\" = example.invalid/image:v1 ] || exit 91\n"
			if tc.fail {
				script += "exit 42\n"
			} else {
				script += "cat <<'MANIFEST'\n" + tc.response + "\nMANIFEST\n"
			}
			if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			got, err := resolveImageDigests(ctx, binary, "example.invalid/image:v1", tc.arches)
			if tc.code != "" {
				assertLifecycleCommandCode(t, err, tc.code)
				if got != nil {
					t.Fatalf("failed resolution exposed partial pins: %v", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pins = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
