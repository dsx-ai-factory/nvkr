// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	goyaml "go.yaml.in/yaml/v2"
	"sigs.k8s.io/yaml"
)

// Synthetic schema exercises the admitted validator features without vendoring
// NVKE's CRD. NVKE_SOURCE opts into the same contracts against the pinned source.
func schemaFixture() object {
	text := func() object { return object{"type": "string"} }
	named := func(max int, pattern string) object {
		return object{"type": "string", "minLength": 1, "maxLength": max, "pattern": pattern}
	}
	record := func(properties object, required ...string) object {
		return object{"type": "object", "properties": properties, "required": required}
	}
	script := named(16384, "")
	script["x-kubernetes-validations"] = []object{{"rule": headerRule}, {"rule": commandRule}, {"rule": delimitersRule}}
	spec := record(object{
		"architecture":    named(32, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
		"buildDate":       object{"type": "string", "format": "date"},
		"variant":         object{"type": "string", "maxLength": 63, "pattern": `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`},
		"nico":            record(object{"ipxeScript": script}, "ipxeScript"),
		"operatingSystem": record(object{"family": named(63, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`), "version": named(32, `^[0-9]+(\.[0-9]+)*$`)}, "family", "version"),
	}, "architecture", "buildDate", "nico", "operatingSystem")
	spec["x-kubernetes-validations"] = []object{{"rule": immutableRule}}
	return record(object{"apiVersion": text(), "kind": text(), "metadata": object{"type": "object"}, "spec": spec}, "spec")
}
func crdFixture(schema object) []byte {
	b, err := yaml.Marshal(object{"kind": "CustomResourceDefinition", "spec": object{"group": "internal.nvke.nvidia.com", "names": object{"kind": "MachineImage"}, "versions": []object{{"name": "v1", "served": true, "schema": object{"openAPIV3Schema": schema}}}}})
	if err != nil {
		panic(err)
	}
	return b
}
func templateFixture(t *testing.T) string {
	t.Helper()
	b, err := read(t.Context(), "machineimage.yaml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func testValidator(t *testing.T) *validator {
	t.Helper()
	var v *validator
	var err error
	if source := os.Getenv("NVKE_SOURCE"); source != "" {
		v, err = loadCRD(t.Context(), source, ".", "")
	} else {
		v, err = parseCRD(crdFixture(schemaFixture()))
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func fixture() (object, []any) {
	disks, imagers, assets, metadata := []any{}, []any{}, []any{}, []any{}
	for _, arch := range []string{"amd64", "arm64"} {
		disk := object{"output": "disk-" + arch, "platform": "linux/" + arch}
		imager := object{"output": "imager-" + arch, "platform": "linux/" + arch, "member": "imager.efi", "profile": "synthetic-strict-v1"}
		disks = append(disks, disk)
		imagers = append(imagers, imager)
		metadata = append(metadata, object{"output": disk["output"], "platform": disk["platform"], "family": "ubuntu", "version": "24.04", "variant": "", "buildDate": "2026-11-19"})
		for _, item := range []struct {
			kind, digest string
			selection    object
		}{{"disk-image", strings.Repeat("a", 64), disk}, {"executable", strings.Repeat("b", 64), imager}} {
			binding := object{"kind": item.kind, "sha256": "sha256:" + item.digest, "size": json.Number("1234")}
			for k, v := range item.selection {
				binding[k] = v
			}
			assets = append(assets, object{"binding": binding, "url": "https://boot.example.test/" + str(binding["output"]) + "/" + item.digest})
		}
	}
	return object{"selection": object{"schemaVersion": "nvkr.dev/boot-selection/v1", "disks": disks, "imagers": imagers}, "destination": object{"name": "primary"}, "assets": assets}, metadata
}
func documents(t *testing.T, raw string) []object {
	t.Helper()
	d := goyaml.NewDecoder(strings.NewReader(raw))
	var result []object
	for {
		var v any
		err := d.Decode(&v)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := goyaml.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		value, err := decodeYAML(b)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, obj(value))
	}
	return result
}
func renderFixture(t *testing.T, p []any, m []any, template string) string {
	t.Helper()
	raw, err := render(p, m, testValidator(t), template)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestBothArchitecturesAndDownstreamPlaceholders(t *testing.T) {
	p, m := fixture()
	rendered := renderFixture(t, []any{p}, m, templateFixture(t))
	docs := documents(t, rendered)
	if len(docs) != 2 {
		t.Fatalf("documents: %d", len(docs))
	}
	for _, d := range docs {
		if err := testValidator(t).validate(d); err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^mi-[a-z2-7]{52}$`).MatchString(str(obj(d["metadata"])["name"])) {
			t.Fatal("invalid name")
		}
		s := obj(d["spec"])
		script := str(obj(s["nico"])["ipxeScript"])
		for _, literal := range []string{"image_sha=" + strings.Repeat("a", 64), "disk_strategy={{ .DiskSelectionStrategy }}", "${cloudinit-url}"} {
			if !strings.Contains(script, literal) {
				t.Fatalf("missing %q", literal)
			}
		}
		if strings.Contains(script, "sha256:") {
			t.Fatal("digest prefix leaked into script")
		}
		console := "ttyAMA0"
		if s["architecture"] == "amd64" {
			console = "ttyS1"
		}
		if !strings.Contains(script, console) || strings.Contains(script, "intel_iommu") != (s["architecture"] == "amd64") {
			t.Fatal("wrong architecture arguments")
		}
		if _, ok := s["variant"]; ok {
			t.Fatal("empty variant retained")
		}
	}
}
func TestCompleteSetAndOrderIndependentBytes(t *testing.T) {
	one, m := fixture()
	two, _ := fixture()
	obj(two["destination"])["name"] = "secondary"
	template := templateFixture(t)
	forward := renderFixture(t, []any{one, two}, m, template)
	slices.Reverse(array(one["assets"]))
	slices.Reverse(array(obj(one["selection"])["disks"]))
	slices.Reverse(m)
	backward := renderFixture(t, []any{two, one}, m, template)
	if forward != backward || len(documents(t, forward)) != 4 {
		t.Fatal("order changed bytes or incomplete set")
	}
}
func TestCRDSchemaRejectsMetadataAndTemplateChanges(t *testing.T) {
	for _, tc := range []struct{ field, bad string }{{"family", "bad family"}, {"version", "latest"}, {"variant", "bad variant"}, {"buildDate", "2026-02-30"}} {
		t.Run(tc.field, func(t *testing.T) {
			p, m := fixture()
			obj(m[1])[tc.field] = tc.bad
			output, err := render([]any{p}, m, testValidator(t), templateFixture(t))
			if err == nil || output != "" || !strings.Contains(err.Error(), "schema") {
				t.Fatalf("output=%q err=%v", output, err)
			}
		})
	}
	p, m := fixture()
	_, err := render([]any{p}, m, testValidator(t), strings.ReplaceAll(templateFixture(t), "architecture: ${architecture}", "architecture: []"))
	if err == nil {
		t.Fatal("accepted wrong type")
	}
}
func TestCompleteSelectionAndMetadataRequired(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(object, []any) ([]any, []any)
	}{
		{"missing asset", func(p object, m []any) ([]any, []any) { p["assets"] = array(p["assets"])[1:]; return []any{p}, m }},
		{"duplicate asset", func(p object, m []any) ([]any, []any) {
			p["assets"] = append(array(p["assets"]), array(p["assets"])[0])
			return []any{p}, m
		}},
		{"duplicate imager", func(p object, m []any) ([]any, []any) {
			s := obj(p["selection"])
			s["imagers"] = append(array(s["imagers"]), array(s["imagers"])[0])
			return []any{p}, m
		}},
		{"digest", func(p object, m []any) ([]any, []any) {
			obj(obj(array(p["assets"])[0])["binding"])["sha256"] = "latest"
			return []any{p}, m
		}},
		{"platform", func(p object, m []any) ([]any, []any) {
			obj(obj(array(p["assets"])[0])["binding"])["platform"] = "linux/riscv64"
			return []any{p}, m
		}},
		{"destination", func(p object, m []any) ([]any, []any) { return []any{p, p}, m }},
		{"missing metadata", func(p object, m []any) ([]any, []any) { return []any{p}, m[:1] }},
		{"extra metadata", func(p object, m []any) ([]any, []any) {
			extra := object{}
			for k, v := range obj(m[0]) {
				extra[k] = v
			}
			extra["output"] = "extra"
			return []any{p}, append(m, extra)
		}},
		{"boolean size", func(p object, m []any) ([]any, []any) {
			obj(obj(array(p["assets"])[0])["binding"])["size"] = true
			return []any{p}, m
		}},
		{"fraction size", func(p object, m []any) ([]any, []any) {
			obj(obj(array(p["assets"])[0])["binding"])["size"] = json.Number("1.5")
			return []any{p}, m
		}},
		{"missing imager profile", func(p object, m []any) ([]any, []any) {
			obj(array(obj(p["selection"])["imagers"])[0])["profile"] = ""
			return []any{p}, m
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, m := fixture()
			publications, metadata := tc.mutate(p, m)
			out, err := render(publications, metadata, testValidator(t), templateFixture(t))
			if err == nil || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}
func TestURLGrammarRejectsScriptInjection(t *testing.T) {
	for _, raw := range []string{"http://host/image", "https://u:p@host/image", "https://host/image?token=x", "https://host/image#x", "https://host/../image", "https://host//image", "https://host/image;reboot", "https://host/image\nboot", "https://host/${inject}", "https://host/{{inject}}", "https://host/%20", "https://host:65536/image", "https://[2001:db8::1]junk/image", "https://host:/image", "https://[bogus:address]/image"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := literalURL(raw); err == nil {
				t.Fatal("accepted URL")
			}
		})
	}
	for _, raw := range []string{"https://[2001:db8::1]/image", "https://host:443/image"} {
		if actual, err := literalURL(raw); err != nil || actual != raw {
			t.Fatalf("%q: %v", raw, err)
		}
	}
}
func git(t *testing.T, repo string, input []byte, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-C", repo}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Synthetic Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Synthetic Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}
func sourceFixture(t *testing.T) (source, catalog, pin string) {
	t.Helper()
	return sourceFixtureData(t, crdFixture(schemaFixture()))
}
func sourceFixtureData(t *testing.T, raw []byte) (source, catalog, pin string) {
	t.Helper()
	root := t.TempDir()
	source = filepath.Join(root, "source.git")
	git(t, root, nil, "init", "--bare", "--object-format=sha1", source)
	blob := strings.TrimSpace(string(git(t, source, raw, "hash-object", "-w", "--stdin")))
	tree := strings.TrimSpace(string(git(t, source, []byte("100644 blob "+blob+"\t"+filepath.Base(crdPath)+"\n"), "mktree")))
	dirs := strings.Split(filepath.Dir(crdPath), "/")
	for i := len(dirs) - 1; i >= 0; i-- {
		tree = strings.TrimSpace(string(git(t, source, []byte("040000 tree "+tree+"\t"+dirs[i]+"\n"), "mktree")))
	}
	pin = strings.TrimSpace(string(git(t, source, []byte("synthetic schema fixture\n"), "-c", "commit.gpgsign=false", "commit-tree", tree)))
	catalog = filepath.Join(root, "catalog", "machineimage")
	if err := os.MkdirAll(catalog, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(catalog, "..", "pins.lock.yaml"), []byte("pins:\n  nvke-controller:\n    commit: "+pin+"\n"))
	write(t, filepath.Join(catalog, "machineimage.yaml.tmpl"), []byte(templateFixture(t)))
	return
}
func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestExactSourcePinRequired(t *testing.T) {
	t.Setenv("GIT_DEFAULT_HASH", "sha256")
	source, catalog, pin := sourceFixture(t)
	for _, asserted := range []string{"", pin} {
		if _, err := loadCRD(t.Context(), source, catalog, asserted); err != nil {
			t.Fatal(err)
		}
	}
	for _, asserted := range []string{"main", strings.Repeat("0", 40), "28b9e338c1d08a4a0427b7c6d79c8174358ad991"} {
		if _, err := loadCRD(t.Context(), source, catalog, asserted); err == nil || !strings.Contains(err.Error(), "differs from the catalog") {
			t.Fatalf("err=%v", err)
		}
	}
	// A valid source cannot rescue a non-commit catalog pin.
	write(t, filepath.Join(catalog, "..", "pins.lock.yaml"), []byte("pins: {nvke-controller: {commit: main}}"))
	if _, err := loadCRD(t.Context(), source, catalog, ""); err == nil {
		t.Fatal("accepted floating catalog pin")
	}
}
func TestChangedExactBytesChangeCandidateName(t *testing.T) {
	p, m := fixture()
	template := templateFixture(t)
	baseline := obj(documents(t, renderFixture(t, []any{p}, m, template))[0]["metadata"])["name"]
	asset := obj(array(p["assets"])[0])
	asset["url"] = str(asset["url"]) + "/changed"
	changed := obj(documents(t, renderFixture(t, []any{p}, m, template))[0]["metadata"])["name"]
	if baseline == changed {
		t.Fatal("bytes changed without candidate name change")
	}
}
func TestMissingPromisorBlobDoesNotFetch(t *testing.T) {
	t.Setenv("GIT_DEFAULT_HASH", "sha256")
	source, catalog, pin := sourceFixture(t)
	root := t.TempDir()
	repo := filepath.Join(root, "partial.git")
	git(t, root, nil, "init", "--bare", "--object-format=sha1", repo)
	objects := [][2]string{{"commit", pin}, {"tree", pin + "^{tree}"}}
	dirs := strings.Split(filepath.Dir(crdPath), "/")
	for i := range dirs {
		objects = append(objects, [2]string{"tree", pin + ":" + strings.Join(dirs[:i+1], "/")})
	}
	for _, item := range objects {
		raw := git(t, source, nil, "cat-file", item[0], item[1])
		git(t, repo, raw, "hash-object", "-w", "-t", item[0], "--stdin")
	}
	git(t, repo, nil, "config", "remote.origin.promisor", "true")
	git(t, repo, nil, "config", "remote.origin.url", filepath.Join(root, "absent-remote.git"))
	trace := filepath.Join(root, "control.trace")
	t.Setenv("GIT_TRACE", trace)
	t.Setenv("GIT_NO_LAZY_FETCH", "0")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--no-replace-objects", "-C", repo, "cat-file", "blob", pin+"^{commit}:"+crdPath)
	if err := cmd.Run(); err == nil {
		t.Fatal("missing blob succeeded")
	}
	b, err := os.ReadFile(trace)
	if err != nil || !bytes.Contains(b, []byte(" fetch ")) {
		t.Fatalf("control did not attempt fetch: %s %v", b, err)
	}
	trace = filepath.Join(root, "offline.trace")
	t.Setenv("GIT_TRACE", trace)
	if _, err := loadCRD(t.Context(), repo, catalog, pin); err == nil {
		t.Fatal("missing blob succeeded")
	}
	b, err = os.ReadFile(trace)
	if err != nil || bytes.Contains(b, []byte(" fetch ")) {
		t.Fatalf("offline tried fetch: %s %v", b, err)
	}
}
func TestCLISuccess(t *testing.T) {
	source, catalog, pin := sourceFixture(t)
	root := t.TempDir()
	pp, mp := filepath.Join(root, "publication.json"), filepath.Join(root, "metadata.json")
	p, m := fixture()
	pb, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write(t, pp, pb)
	write(t, mp, mb)
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"--nvke-source", source, "--nvke-commit", pin, "--catalog-dir", catalog, "--publication", pp, "--metadata", mp}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := len(documents(t, stdout.String())); got != 2 {
		t.Fatalf("got %d documents, want 2", got)
	}
	v, err := loadCRD(t.Context(), source, catalog, pin)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := render([]any{p}, m, v, templateFixture(t))
	if err != nil || stdout.String() != expected {
		t.Fatalf("CLI output differs from validated render: %v", err)
	}
}

func TestCLIFailureHasNoPartialStdout(t *testing.T) {
	source, catalog, pin := sourceFixture(t)
	root := t.TempDir()
	pp, mp := filepath.Join(root, "publication.json"), filepath.Join(root, "metadata.json")
	for _, tc := range []struct {
		name       string
		mutate     func(object, []any)
		diagnostic string
	}{
		{"schema", func(p object, m []any) { obj(m[1])["version"] = "invalid" }, "schema"},
		{"missing asset", func(p object, m []any) { p["assets"] = array(p["assets"])[1:] }, "missing selected asset"},
		{"private input", func(p object, m []any) { p["selection"] = "synthetic-private-input-must-not-appear" }, "unknown selection schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, m := fixture()
			tc.mutate(p, m)
			pb, _ := json.Marshal(p)
			mb, _ := json.Marshal(m)
			write(t, pp, pb)
			write(t, mp, mb)
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), []string{"--nvke-source", source, "--nvke-commit", pin, "--catalog-dir", catalog, "--publication", pp, "--metadata", mp}, &stdout, &stderr)
			if code != 1 || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "verify-failed: ") || !strings.Contains(stderr.String(), tc.diagnostic) || strings.Contains(stderr.String(), "synthetic-private") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}
func TestCELAndSchemaAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(object)
	}{
		{"unknown rule", func(s object) { s["x-kubernetes-validations"] = []object{{"rule": "self != oldSelf"}} }},
		{"wrong location", func(s object) { s["x-kubernetes-validations"] = []object{{"rule": headerRule}} }},
		{"dotted property location", func(s object) {
			obj(s["properties"])["spec.nico.ipxeScript"] = object{"type": "string", "x-kubernetes-validations": []object{{"rule": headerRule}}}
		}},
		{"optional old self", func(s object) {
			obj(obj(s["properties"])["spec"])["x-kubernetes-validations"] = []object{{"rule": immutableRule, "optionalOldSelf": true}}
		}},
		{"reference", func(s object) { s["$ref"] = "https://example.invalid/schema" }},
		{"extension", func(s object) { s["x-kubernetes-preserve-unknown-fields"] = true }},
		{"pattern", func(s object) { obj(obj(s["properties"])["apiVersion"])["pattern"] = "[" }},
		{"unknown format", func(s object) { obj(obj(s["properties"])["apiVersion"])["format"] = "unknown" }},
		{"schema type", func(s object) { s["type"] = "array" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := schemaFixture()
			tc.mutate(schema)
			if _, err := parseCRD(crdFixture(schema)); err == nil {
				t.Fatal("accepted unsupported schema")
			}
		})
	}
	for _, tc := range []struct {
		name, script string
		valid        bool
	}{
		{"LF", "#!ipxe\nboot", true}, {"CRLF", "#!ipxe\r\nboot", true}, {"header", "boot", false}, {"empty command", "#!ipxe\n \t\n", false}, {"delimiters", "#!ipxe\n{{ boot", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, m := fixture()
			docs := documents(t, renderFixture(t, []any{p}, m, templateFixture(t)))
			obj(obj(docs[0]["spec"])["nico"])["ipxeScript"] = tc.script
			err := testValidator(t).validate(docs[0])
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v", err)
			}
		})
	}
	// Rules in a non-served version are still guarded.
	root, err := decodeYAML(crdFixture(schemaFixture()))
	if err != nil {
		t.Fatal(err)
	}
	spec := obj(obj(root)["spec"])
	spec["versions"] = append(array(spec["versions"]), object{"name": "future", "served": false, "schema": object{"x-kubernetes-validations": []object{{"rule": "false"}}}})
	raw, _ := yaml.Marshal(root)
	if _, err := parseCRD(raw); err == nil {
		t.Fatal("ignored unknown rule in another version")
	}
}
func TestMalformedInputs(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{} {}`, `{"x":`, "\xff", strings.Repeat("[", 66) + strings.Repeat("]", 66)} {
		t.Run(fmt.Sprintf("json-%x", raw[:min(len(raw), 12)]), func(t *testing.T) {
			if _, err := decodeJSON([]byte(raw)); err == nil {
				t.Fatal("accepted malformed JSON")
			}
		})
	}
	for _, raw := range []string{"x: 1\nx: 2\n", "x: 1\n---\nx: 2\n", "\xff"} {
		if _, err := decodeYAML([]byte(raw)); err == nil {
			t.Fatal("accepted malformed YAML")
		}
	}
	for _, placeholder := range []string{"${absent}", "${}", "${", "$", "${family"} {
		p, m := fixture()
		if _, err := render([]any{p}, m, testValidator(t), templateFixture(t)+"\nunknown: "+placeholder+"\n"); err == nil {
			t.Fatal("accepted invalid substitution")
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "large")
	write(t, path, bytes.Repeat([]byte{'x'}, limit+1))
	if _, err := read(t.Context(), path); err == nil {
		t.Fatal("accepted oversized input")
	}
}
func TestSchemaPanicFailsClosed(t *testing.T) {
	v := testValidator(t)
	if err := v.schema.Ref.UnmarshalJSON([]byte(`{"$ref":"https://example.invalid/schema"}`)); err != nil {
		t.Fatal(err)
	}
	p, m := fixture()
	out, err := render([]any{p}, m, v, templateFixture(t))
	if err == nil || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestYAMLStringKeys(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"ambiguous integer", "1: first\n\"1\": second\n", false},
		{"nested boolean", "annotations:\n  true: first\n  \"true\": second\n", false},
		{"sequence map", "items:\n- 1: value\n", false},
		{"quoted keys", "\"1\": first\n\"true\": second\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := decodeYAML([]byte(tc.raw))
			if (err == nil) != tc.valid {
				t.Fatalf("value=%v err=%v", value, err)
			}
			if tc.valid && (obj(value)["1"] != "first" || obj(value)["true"] != "second") {
				t.Fatal("quoted keys changed")
			}
		})
	}
}

func TestGitBlobOutputLimit(t *testing.T) {
	for _, size := range []int{limit, limit + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			source, _, pin := sourceFixtureData(t, bytes.Repeat([]byte{'x'}, size))
			raw, err := gitBlob(t.Context(), source, pin)
			if size <= limit {
				if err != nil || len(raw) != size {
					t.Fatalf("bytes=%d err=%v", len(raw), err)
				}
			} else if err == nil || len(raw) != 0 {
				t.Fatalf("oversize bytes=%d err=%v", len(raw), err)
			}
		})
	}
}

func TestPinnedTagObjectRejected(t *testing.T) {
	source, catalog, pin := sourceFixture(t)
	tag := []byte("object " + pin + "\ntype commit\ntag synthetic\ntagger Synthetic Fixture <fixture@example.invalid> 0 +0000\n\nannotated fixture\n")
	tagID := strings.TrimSpace(string(git(t, source, tag, "hash-object", "-w", "-t", "tag", "--stdin")))
	write(t, filepath.Join(catalog, "..", "pins.lock.yaml"), []byte("pins:\n  nvke-controller:\n    commit: "+tagID+"\n"))
	if _, err := loadCRD(t.Context(), source, catalog, tagID); err == nil {
		t.Fatal("accepted annotated tag object as commit pin")
	}
}

func TestGitOfflineCapabilityRequired(t *testing.T) {
	root := t.TempDir()
	trace := filepath.Join(root, "trace")
	stub := filepath.Join(root, "git")
	write(t, stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$GIT_CAPABILITY_TRACE\"\nexit 129\n"))
	if err := os.Chmod(stub, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CAPABILITY_TRACE", trace)
	raw, err := gitBlob(t.Context(), "must-not-read-source", strings.Repeat("a", 40))
	if err == nil || len(raw) != 0 || !strings.Contains(err.Error(), "--no-lazy-fetch") {
		t.Fatalf("bytes=%d err=%v", len(raw), err)
	}
	invoked, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(invoked), "cat-file") || !strings.Contains(string(invoked), "--no-lazy-fetch --version") {
		t.Fatalf("source operation before capability admission: %s", invoked)
	}
}

func TestOutputNamesMatchBootSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"disk.debug", true}, {"disk_debug", true}, {"disk..debug", true}, {"disk-1", true}, {strings.Repeat("a", 128), true},
		{".disk", false}, {"disk.", false}, {"disk/part", false}, {"Disk", false}, {strings.Repeat("a", 129), false},
	} {
		for _, kind := range []string{"disk", "imager"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				p, m := fixture()
				original := kind + "-amd64"
				for _, field := range []string{"disks", "imagers"} {
					for _, item := range array(obj(p["selection"])[field]) {
						if obj(item)["output"] == original {
							obj(item)["output"] = tc.name
						}
					}
				}
				for _, item := range array(p["assets"]) {
					binding := obj(obj(item)["binding"])
					if binding["output"] == original {
						binding["output"] = tc.name
					}
				}
				for _, item := range m {
					if obj(item)["output"] == original {
						obj(item)["output"] = tc.name
					}
				}
				out, err := render([]any{p}, m, testValidator(t), templateFixture(t))
				if (err == nil) != tc.valid {
					t.Fatalf("err=%v", err)
				}
				if !tc.valid && out != "" {
					t.Fatal("partial output")
				}
			})
		}
	}
}
