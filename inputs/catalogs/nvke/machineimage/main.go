// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// This offline catalog experiment consumes descriptions, not authenticated receipts.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	goyaml "go.yaml.in/yaml/v2"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"
	"sigs.k8s.io/yaml"
)

const crdPath = "management-engine/config/crd/bases/internal.nvke.nvidia.com_machineimages.yaml"
const limit = 4 * 1024 * 1024

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

// Match Definition BootSelection output names; destinations keep DNS-label syntax.
var outputNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var urlPattern = regexp.MustCompile(`^[A-Za-z0-9._~:/\[\]-]+$`)
var placeholderPattern = regexp.MustCompile(`^\$(?:\$|\{[A-Za-z_][A-Za-z0-9_]*\}|[A-Za-z_][A-Za-z0-9_]*)`)

type object = map[string]any

func obj(v any) object            { o, _ := v.(map[string]any); return o }
func str(v any) string            { s, _ := v.(string); return s }
func array(v any) []any           { a, _ := v.([]any); return a }
func reject(message string) error { return errors.New(message) }

// Duplicate names, invalid UTF-8 and trailing documents must not be normalized away.
func decodeJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, reject("input is not UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) (any, error)
	value = func(depth int) (any, error) {
		if depth > 64 {
			return nil, reject("input nesting exceeds limit")
		}
		token, err := d.Token()
		if err != nil {
			return nil, reject("invalid JSON")
		}
		switch token {
		case json.Delim('{'):
			m := object{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, reject("invalid JSON")
				}
				k, ok := key.(string)
				if !ok {
					return nil, reject("invalid JSON key")
				}
				if _, exists := m[k]; exists {
					return nil, reject("duplicate JSON key")
				}
				v, err := value(depth + 1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, reject("invalid JSON object")
			}
			return m, nil
		case json.Delim('['):
			a := []any{}
			for d.More() {
				v, err := value(depth + 1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, reject("invalid JSON array")
			}
			return a, nil
		default:
			if _, ok := token.(json.Delim); ok {
				return nil, reject("invalid JSON")
			}
			return token, nil
		}
	}
	v, err := value(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, reject("trailing JSON input")
	}
	return v, nil
}

func decodeYAML(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, reject("input is not UTF-8")
	}
	decoder := goyaml.NewDecoder(bytes.NewReader(raw))
	decoder.SetStrict(true)
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, reject("invalid YAML")
	}
	if err := checkYAMLKeys(document, 0); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&document); err != io.EOF {
		return nil, reject("expected one YAML document")
	}
	b, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return nil, reject("invalid YAML")
	}
	return decodeJSON(b)
}

// YAML permits non-string keys that JSON conversion would stringify and merge.
func checkYAMLKeys(value any, depth int) error {
	if depth > 64 {
		return reject("input nesting exceeds limit")
	}
	switch node := value.(type) {
	case map[any]any:
		for key, child := range node {
			if _, ok := key.(string); !ok {
				return reject("YAML mapping keys must be strings")
			}
			if err := checkYAMLKeys(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range node {
			if err := checkYAMLKeys(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func read(ctx context.Context, path string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, reject("operation canceled")
	}
	// Reject named pipes and devices before opening them; opening a FIFO can block.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, reject("input must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, reject("cannot read input")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, reject("input must be a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, reject("cannot read input")
	}
	if len(raw) > limit {
		return nil, reject("input exceeds 4 MiB")
	}
	return raw, nil
}

// Cap subprocess buffers while exec.CommandContext owns termination and joining.
// Do not embed bytes.Buffer: its promoted ReadFrom lets io.Copy bypass Write.
type cappedBuffer struct{ buffer bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > limit-b.buffer.Len() {
		return 0, reject("Git output exceeds 4 MiB")
	}
	return b.buffer.Write(p)
}
func gitBlob(ctx context.Context, source, pin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Older Git silently ignores GIT_NO_LAZY_FETCH. Admit the explicit option
	// without accessing the source, then retain it on every object operation.
	if _, err := gitOutput(ctx, "--version"); err != nil {
		return nil, reject("Git with --no-lazy-fetch support (2.45+) is required")
	}
	kind, err := gitOutput(ctx, "-C", source, "cat-file", "-t", pin)
	if err != nil {
		return nil, err
	}
	if string(kind) != "commit\n" {
		return nil, reject("catalog NVKE source pin must directly identify a commit object")
	}
	return gitOutput(ctx, "-C", source, "cat-file", "blob", pin+"^{commit}:"+crdPath)
}

func gitOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--no-lazy-fetch"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	cmd.WaitDelay = time.Second
	var stdout, stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, reject("cannot read pinned source CRD")
	}
	return stdout.buffer.Bytes(), nil
}
func catalogPin(ctx context.Context, catalog string, asserted string) (string, error) {
	raw, err := read(ctx, filepath.Join(catalog, "..", "pins.lock.yaml"))
	if err != nil {
		return "", err
	}
	v, err := decodeYAML(raw)
	if err != nil {
		return "", err
	}
	pin := str(obj(obj(obj(v)["pins"])["nvke-controller"])["commit"])
	if !commitPattern.MatchString(pin) {
		return "", reject("catalog NVKE source must be commit-pinned")
	}
	if asserted != "" && asserted != pin {
		return "", reject("NVKE commit differs from the catalog controller pin")
	}
	return pin, nil
}
func loadCRD(ctx context.Context, source, catalog, asserted string) (*validator, error) {
	pin, err := catalogPin(ctx, catalog, asserted)
	if err != nil {
		return nil, err
	}
	raw, err := gitBlob(ctx, source, pin)
	if err != nil {
		return nil, err
	}
	return parseCRD(raw)
}

const headerRule = `self.startsWith('#!ipxe\n') || self.startsWith('#!ipxe\r\n')`
const commandRule = `self.replace('#!ipxe', '').trim() != ''`
const delimitersRule = `self.split('{{').size() == self.split('}}').size()`
const immutableRule = `self == oldSelf`

type celRule struct {
	path      []string
	predicate func(string) bool
}

// Exact text AND schema location are guarded. This program only creates candidates;
// oldSelf is absent, so the known transition rule is inapplicable on creation.
var celRules = map[string]celRule{
	headerRule:     {[]string{"spec", "nico", "ipxeScript"}, func(s string) bool { return strings.HasPrefix(s, "#!ipxe\n") || strings.HasPrefix(s, "#!ipxe\r\n") }},
	commandRule:    {[]string{"spec", "nico", "ipxeScript"}, func(s string) bool { return strings.TrimSpace(strings.ReplaceAll(s, "#!ipxe", "")) != "" }},
	delimitersRule: {[]string{"spec", "nico", "ipxeScript"}, func(s string) bool { return strings.Count(s, "{{") == strings.Count(s, "}}") }},
	immutableRule:  {[]string{"spec"}, nil},
}

type boundRule struct {
	path []string
	rule celRule
}
type validator struct {
	schema spec.Schema
	rules  []boundRule
}

// Reject every unknown rule anywhere in the CRD, including unselected versions.
func guardRules(v any) error {
	switch node := v.(type) {
	case map[string]any:
		for key, value := range node {
			if key == "$ref" {
				return reject("CRD schema references are unsupported")
			}
			if key == "x-kubernetes-validations" {
				rules, ok := value.([]any)
				if !ok {
					return reject("invalid CEL rule table")
				}
				for _, r := range rules {
					record := obj(r)
					if _, ok := celRules[str(record["rule"])]; !ok {
						return reject("unknown CRD CEL rule")
					}
					for k := range record {
						if k != "rule" && k != "message" {
							return reject("unsupported CEL rule option")
						}
					}
				}
			}
			if err := guardRules(value); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range node {
			if err := guardRules(item); err != nil {
				return err
			}
		}
	}
	return nil
}

// Admit the small structural subset used by the pinned CRD. kube-openapi is a
// schema validator, not the Kubernetes API server: unsupported extensions must
// not silently disappear during unmarshalling. New features require review.
func (v *validator) admitSchema(node object, path []string) error {
	if node == nil {
		return reject("missing CRD schema")
	}
	typ := str(node["type"])
	if typ != "string" && typ != "object" {
		return reject("unsupported CRD schema type")
	}
	for k, value := range node {
		switch k {
		case "type", "description":
			if _, ok := value.(string); !ok {
				return reject("invalid CRD schema string")
			}
		case "format":
			if typ != "string" || value != "date" {
				return reject("unsupported CRD format")
			}
		case "pattern":
			pattern, ok := value.(string)
			if !ok || typ != "string" {
				return reject("invalid CRD pattern")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return reject("invalid CRD pattern")
			}
		case "minLength", "maxLength":
			n, ok := value.(json.Number)
			if !ok || typ != "string" {
				return reject("invalid CRD length")
			}
			i, err := n.Int64()
			if err != nil || i < 0 {
				return reject("invalid CRD length")
			}
		case "required":
			a, ok := value.([]any)
			if !ok || typ != "object" {
				return reject("invalid CRD required fields")
			}
			seen := map[string]bool{}
			for _, item := range a {
				s, ok := item.(string)
				if !ok || seen[s] || obj(node["properties"])[s] == nil {
					return reject("invalid CRD required field")
				}
				seen[s] = true
			}
		case "properties":
			properties := obj(value)
			if properties == nil || typ != "object" {
				return reject("invalid CRD properties")
			}
			for name, child := range properties {
				if err := v.admitSchema(obj(child), append(slices.Clone(path), name)); err != nil {
					return err
				}
			}
		case "x-kubernetes-validations":
			for _, item := range array(value) {
				rule := celRules[str(obj(item)["rule"])]
				if !slices.Equal(rule.path, path) {
					return reject("unsupported CEL rule location")
				}
				v.rules = append(v.rules, boundRule{slices.Clone(path), rule})
			}
		default:
			return reject("unsupported CRD schema feature")
		}
	}
	return nil
}
func parseCRD(raw []byte) (result *validator, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = reject("invalid source CRD schema")
		}
	}()
	root, err := decodeYAML(raw)
	if err != nil {
		return nil, err
	}
	if err := guardRules(root); err != nil {
		return nil, err
	}
	crd := obj(root)
	s := obj(crd["spec"])
	if crd["kind"] != "CustomResourceDefinition" || s["group"] != "internal.nvke.nvidia.com" || obj(s["names"])["kind"] != "MachineImage" {
		return nil, reject("unexpected source CRD")
	}
	var schema object
	count := 0
	for _, value := range array(s["versions"]) {
		version := obj(value)
		if version["name"] == "v1" && version["served"] == true {
			schema = obj(obj(version["schema"])["openAPIV3Schema"])
			count++
		}
	}
	if count != 1 {
		return nil, reject("source must serve exactly one v1 schema")
	}
	v := &validator{}
	if err := v.admitSchema(schema, nil); err != nil {
		return nil, err
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, reject("invalid source CRD schema")
	}
	if err := json.Unmarshal(b, &v.schema); err != nil {
		return nil, reject("invalid source CRD schema")
	}
	// Exercise construction while still inside panic recovery.
	validate.NewSchemaValidator(&v.schema, nil, "", strfmt.Default)
	return v, nil
}
func (v *validator) validate(document object) (err error) {
	defer func() {
		if recover() != nil {
			err = reject("invalid source CRD schema")
		}
	}()
	if err := validate.AgainstSchema(&v.schema, document, strfmt.Default); err != nil {
		return reject("candidate does not satisfy source CRD schema")
	}
	for _, bound := range v.rules {
		var value any = document
		for _, part := range bound.path {
			value = obj(value)[part]
		}
		if value == nil || bound.rule.predicate == nil {
			continue
		}
		s, ok := value.(string)
		if !ok || !bound.rule.predicate(s) {
			return reject("candidate does not satisfy source CRD CEL rule")
		}
	}
	return nil
}

func literalURL(value any) (string, error) {
	raw, ok := value.(string)
	if !ok || !urlPattern.MatchString(raw) {
		return "", reject("unsafe URL literal")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || !strings.HasPrefix(u.Path, "/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", reject("URL must be literal absolute HTTPS")
	}
	for _, part := range strings.Split(u.Path, "/")[1:] {
		if part == "" || part == "." || part == ".." {
			return "", reject("ambiguous URL path")
		}
	}
	authority := strings.ToLower(u.Hostname())
	if strings.Contains(authority, ":") {
		address, err := netip.ParseAddr(authority)
		if err != nil || !address.Is6() {
			return "", reject("invalid URL address")
		}
		authority = "[" + authority + "]"
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", reject("invalid URL port")
		}
		authority += ":" + strconv.Itoa(n)
	}
	if u.Host != authority {
		return "", reject("ambiguous URL authority")
	}
	return raw, nil
}

type assetKey struct{ output, platform string }

func key(record object) assetKey { return assetKey{str(record["output"]), str(record["platform"])} }
func selectedAssets(publication object) (map[assetKey]object, error) {
	selection := obj(publication["selection"])
	if selection["schemaVersion"] != "nvkr.dev/boot-selection/v1" {
		return nil, reject("unknown selection schema")
	}
	type expectedAsset struct {
		kind      string
		selection object
	}
	expected := map[assetKey]expectedAsset{}
	for _, group := range []struct{ field, kind string }{{"disks", "disk-image"}, {"imagers", "executable"}} {
		entries, ok := selection[group.field].([]any)
		if !ok {
			return nil, reject("invalid selection")
		}
		for _, entry := range entries {
			item := obj(entry)
			k := key(item)
			if _, exists := expected[k]; exists {
				return nil, reject("duplicate selection")
			}
			expected[k] = expectedAsset{group.kind, item}
		}
	}
	assets := map[assetKey]object{}
	for _, entry := range array(publication["assets"]) {
		asset := obj(entry)
		binding := obj(asset["binding"])
		k := key(binding)
		wanted, ok := expected[k]
		if !ok || assets[k] != nil {
			return nil, reject("extra or duplicate asset")
		}
		if binding["kind"] != wanted.kind {
			return nil, reject("asset differs from selection")
		}
		for field, value := range wanted.selection {
			if !reflect.DeepEqual(binding[field], value) {
				return nil, reject("asset differs from selection")
			}
		}
		if k.platform != "linux/amd64" && k.platform != "linux/arm64" {
			return nil, reject("unsupported platform")
		}
		if !outputNamePattern.MatchString(k.output) || len(k.output) > 128 {
			return nil, reject("invalid output name")
		}
		n, ok := binding["size"].(json.Number)
		if !ok {
			return nil, reject("invalid raw asset binding")
		}
		size, err := n.Int64()
		if err != nil || size <= 0 || !digestPattern.MatchString(str(binding["sha256"])) {
			return nil, reject("invalid raw asset binding")
		}
		if _, err := literalURL(asset["url"]); err != nil {
			return nil, err
		}
		assets[k] = asset
	}
	if len(assets) != len(expected) {
		return nil, reject("missing selected asset")
	}
	return assets, nil
}
func quote(v string) string { b, _ := json.Marshal(v); return string(b) }
func expandTemplate(template string, args map[string]string) (string, error) {
	// os.Expand silently drops malformed braced names. Validate the maintained
	// template's dollar grammar first, preserving Template's $$ escape behavior.
	for rest := template; ; {
		i := strings.IndexByte(rest, '$')
		if i < 0 {
			break
		}
		match := placeholderPattern.FindString(rest[i:])
		if match == "" {
			return "", reject("invalid template placeholder")
		}
		rest = rest[i+len(match):]
	}
	var expandErr error
	rendered := os.Expand(template, func(name string) string {
		if name == "$" {
			return "$"
		}
		value, ok := args[name]
		if !ok {
			expandErr = reject("unknown template placeholder")
		}
		return value
	})
	return rendered, expandErr
}
func render(publications []any, metadata []any, v *validator, template string) (string, error) {
	if len(publications) == 0 || len(publications) > 32 {
		return "", reject("expected 1..32 destination descriptions")
	}
	meta := map[assetKey]object{}
	for _, value := range metadata {
		item := obj(value)
		if len(item) != 6 {
			return "", reject("unexpected metadata fields")
		}
		for _, field := range []string{"output", "platform", "family", "version", "variant", "buildDate"} {
			if _, ok := item[field].(string); !ok {
				return "", reject("metadata must contain six string fields")
			}
		}
		k := key(item)
		if meta[k] != nil {
			return "", reject("duplicate disk metadata")
		}
		meta[k] = item
	}
	type pair struct{ output, platform, destination string }
	documents := map[pair][]byte{}
	used := map[assetKey]bool{}
	destinations := map[string]bool{}
	names := map[string]bool{}
	for _, value := range publications {
		publication := obj(value)
		destination := str(obj(publication["destination"])["name"])
		if !namePattern.MatchString(destination) || len(destination) > 63 {
			return "", reject("invalid destination")
		}
		if destinations[destination] {
			return "", reject("duplicate destination")
		}
		destinations[destination] = true
		assets, err := selectedAssets(publication)
		if err != nil {
			return "", err
		}
		selection := obj(publication["selection"])
		disks, imagers := array(selection["disks"]), array(selection["imagers"])
		if len(disks) == 0 || len(disks) > 256 {
			return "", reject("expected 1..256 disks")
		}
		diskPlatforms := map[string]bool{}
		for _, disk := range disks {
			diskPlatforms[key(obj(disk)).platform] = true
		}
		imagerPlatforms := map[string]bool{}
		for _, imager := range imagers {
			imagerPlatforms[key(obj(imager)).platform] = true
		}
		if !reflect.DeepEqual(diskPlatforms, imagerPlatforms) {
			return "", reject("unused or missing imager platform")
		}
		for _, value := range disks {
			if len(documents) >= 256 {
				return "", reject("candidate set exceeds 256 pairs")
			}
			disk := obj(value)
			k := key(disk)
			var match object
			count := 0
			for _, value := range imagers {
				item := obj(value)
				if key(item).platform == k.platform {
					match = item
					count++
				}
			}
			if count != 1 {
				return "", reject("expected one imager per architecture")
			}
			if str(match["member"]) == "" || str(match["profile"]) == "" {
				return "", reject("imager member/profile missing")
			}
			image, imager := assets[k], assets[key(match)]
			if image["url"] == imager["url"] {
				return "", reject("disk and imager URLs must differ")
			}
			values := meta[k]
			if values == nil {
				return "", reject("missing disk metadata")
			}
			used[k] = true
			arch := strings.TrimPrefix(k.platform, "linux/")
			args := map[string]string{
				"architecture": quote(arch), "output": quote(k.output), "destination": quote(destination),
				"imager_sha": quote(str(obj(imager["binding"])["sha256"])),
				"disk_url":   str(image["url"]), "imager_url": str(imager["url"]),
				"disk_sha": strings.TrimPrefix(str(obj(image["binding"])["sha256"]), "sha256:"),
				"console":  "console=tty0 console=ttyAMA0,115200", "kernel": "loglevel=1 iommu.passthrough=1",
			}
			if arch == "amd64" {
				args["console"] = "console=tty0 console=ttyS1,115200"
				args["kernel"] = "pci=realloc=off reboot=pci loglevel=1 iommu=pt intel_iommu=on"
			}
			for _, field := range []string{"family", "version", "variant", "buildDate"} {
				args[field] = quote(str(values[field]))
			}
			rendered, err := expandTemplate(template, args)
			if err != nil {
				return "", err
			}
			parsed, err := decodeYAML([]byte(rendered))
			if err != nil {
				return "", err
			}
			document := obj(parsed)
			if document["apiVersion"] != "internal.nvke.nvidia.com/v1" || document["kind"] != "MachineImage" {
				return "", reject("template has wrong type")
			}
			s, m := obj(document["spec"]), obj(document["metadata"])
			if s == nil || m == nil {
				return "", reject("invalid template object")
			}
			if values["variant"] == "" {
				delete(s, "variant")
			}
			raw, err := yaml.Marshal(document)
			if err != nil {
				return "", reject("cannot serialize candidate")
			}
			// Exact blob fingerprint for a candidate name, not a structured NVKR identity.
			sum := sha256.Sum256(raw)
			name := "mi-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
			m["name"] = name
			if err := v.validate(document); err != nil {
				return "", err
			}
			if names[name] {
				return "", reject("candidate name collision")
			}
			names[name] = true
			raw, err = yaml.Marshal(document)
			if err != nil {
				return "", reject("cannot serialize candidate")
			}
			documents[pair{k.output, k.platform, destination}] = raw
		}
	}
	if len(used) != len(meta) {
		return "", reject("unused disk metadata")
	}
	keys := make([]pair, 0, len(documents))
	for k := range documents {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b pair) int {
		if c := strings.Compare(a.output, b.output); c != 0 {
			return c
		}
		if c := strings.Compare(a.platform, b.platform); c != 0 {
			return c
		}
		return strings.Compare(a.destination, b.destination)
	})
	var output strings.Builder
	for i, k := range keys {
		if i > 0 {
			output.WriteString("---\n")
		}
		output.Write(documents[k])
	}
	return output.String(), nil
}

type paths []string

func (p *paths) String() string         { return "" }
func (p *paths) Set(value string) error { *p = append(*p, value); return nil }
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "verify-failed: "+err.Error()); return 1 }
	flags := flag.NewFlagSet("machineimage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("nvke-source", "", "local NVKE Git object repository; no fetch")
	commit := flags.String("nvke-commit", "", "optional assertion of the catalog controller pin")
	metadataPath := flags.String("metadata", "", "proposed disk metadata JSON array")
	catalog := flags.String("catalog-dir", ".", "directory containing template and adjacent catalog pins")
	var publications paths
	flags.Var(&publications, "publication", "generic boot description JSON (repeat per destination)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stdout)
			flags.PrintDefaults()
			return 0
		}
		return fail(reject("invalid arguments; use --help"))
	}
	if *source == "" || *metadataPath == "" || len(publications) == 0 || len(publications) > 32 || flags.NArg() != 0 {
		return fail(reject("expected source, metadata and 1..32 publications"))
	}
	v, err := loadCRD(ctx, *source, *catalog, *commit)
	if err != nil {
		return fail(err)
	}
	readJSON := func(path string) (any, error) {
		b, err := read(ctx, path)
		if err != nil {
			return nil, err
		}
		return decodeJSON(b)
	}
	descriptions := make([]any, 0, len(publications))
	for _, path := range publications {
		value, err := readJSON(path)
		if err != nil {
			return fail(err)
		}
		descriptions = append(descriptions, value)
	}
	metadata, err := readJSON(*metadataPath)
	if err != nil {
		return fail(err)
	}
	if _, ok := metadata.([]any); !ok {
		return fail(reject("metadata must be an array"))
	}
	template, err := read(ctx, filepath.Join(*catalog, "machineimage.yaml.tmpl"))
	if err != nil {
		return fail(err)
	}
	output, err := render(descriptions, array(metadata), v, string(template))
	if err != nil {
		return fail(err)
	}
	if ctx.Err() != nil {
		return fail(reject("operation canceled"))
	}
	if _, err := io.WriteString(stdout, output); err != nil {
		return fail(reject("cannot write candidates"))
	}
	return 0
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
