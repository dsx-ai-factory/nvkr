package catalog

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestExtractPinSetVersions(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		paths   map[string]string
		want    map[string]string
		wantErr string
	}{
		{
			name:  "Helm lock versions",
			raw:   "dependencies:\n  - name: cwe\n    version: 0.9.3\n  - name: spire\n    version: v1.2.3-rc.1+build.2\ngenerated: 2026-09-29T12:00:00Z\n",
			paths: map[string]string{"controller": "dependencies[name=cwe].version", "agent": "dependencies[name=spire].version"},
			want:  map[string]string{"controller": "0.9.3", "agent": "v1.2.3-rc.1+build.2"},
		},
		{
			name:  "CycloneDX JSON versions",
			raw:   `{"bomFormat":"CycloneDX","components":[{"bom-ref":"urn:uuid:1234","version":"1.2.3"},{"bom-ref":"spire.agent","version":"2.3.4"}]}`,
			paths: map[string]string{"cwe": "components[bom-ref=urn:uuid:1234].version", "spire": "components[bom-ref=spire.agent].version"},
			want:  map[string]string{"cwe": "1.2.3", "spire": "2.3.4"},
		},
		{
			name:  "scalar paths permit unrelated metadata",
			raw:   "release: {controller: {version: 1.2.3}, metadata: {channel: stable}}\n",
			paths: map[string]string{"cwe": "release.controller.version"},
			want:  map[string]string{"cwe": "1.2.3"},
		},
		{
			name:  "nested selectors each account for complete collection",
			raw:   "groups: [{name: controllers, components: [{name: cwe, version: 1.2.3}, {name: api, version: 2.3.4}]}, {name: nodes, components: [{name: agent, version: 3.4.5}]}]",
			paths: map[string]string{"cwe": "groups[name=controllers].components[name=cwe].version", "api": "groups[name=controllers].components[name=api].version", "agent": "groups[name=nodes].components[name=agent].version"},
			want:  map[string]string{"cwe": "1.2.3", "api": "2.3.4", "agent": "3.4.5"},
		},
		{
			name:  "one upstream version may supply multiple declared targets",
			raw:   "dependencies: [{name: cwe, version: 1.2.3}]",
			paths: map[string]string{"cwe": "dependencies[name=cwe].version", "other": "dependencies[name=cwe].version"},
			want:  map[string]string{"cwe": "1.2.3", "other": "1.2.3"},
		},
		{name: "unknown member", raw: "dependencies: [{name: cwe, version: 1.2.3}, {name: new, version: 2.3.4}]", wantErr: "unaccounted member"},
		{name: "missing member", raw: "dependencies: [{name: other, version: 1.2.3}]", wantErr: "no member"},
		{name: "ambiguous member", raw: "dependencies: [{name: cwe, version: 1.2.3}, {name: cwe, version: 2.3.4}]", wantErr: "duplicate name"},
		{name: "duplicate identity even for unknown members", raw: "dependencies: [{name: cwe, version: 1.2.3}, {name: new, version: 2.3.4}, {name: new, version: 2.3.4}]", wantErr: "duplicate name"},
		{name: "missing identity", raw: "dependencies: [{version: 1.2.3}]", wantErr: "identity"},
		{name: "numeric identity", raw: "dependencies: [{name: 123, version: 1.2.3}]", wantErr: "identity"},
		{name: "boolean identity", raw: "dependencies: [{name: true, version: 1.2.3}]", wantErr: "identity"},
		{name: "surrounding whitespace in identity", raw: `dependencies: [{name: " cwe ", version: 1.2.3}]`, wantErr: "identity"},
		{name: "scalar record", raw: "dependencies: [cwe]", wantErr: "identity"},
		{name: "missing version", raw: "dependencies: [{name: cwe}]", wantErr: "missing object field"},
		{name: "integer version", raw: "dependencies: [{name: cwe, version: 1}]", wantErr: "exact string version"},
		{name: "float version", raw: "dependencies: [{name: cwe, version: 1.2}]", wantErr: "exact string version"},
		{name: "null version", raw: "dependencies: [{name: cwe, version: null}]", wantErr: "exact string version"},
		{name: "object version", raw: "dependencies: [{name: cwe, version: {x: 1.2.3}}]", wantErr: "exact string version"},
		{name: "version range", raw: `dependencies: [{name: cwe, version: "^1.2.3"}]`, wantErr: "exact string version"},
		{name: "version template", raw: `dependencies: [{name: cwe, version: "{{ .Version }}"}]`, wantErr: "exact string version"},
		{name: "version whitespace", raw: `dependencies: [{name: cwe, version: "1.2.3 "}]`, wantErr: "exact string version"},
		{name: "duplicate YAML mapping key", raw: "dependencies: [{name: cwe, version: 1.2.3, version: 2.3.4}]", wantErr: "duplicate key"},
		{name: "duplicate JSON mapping key", raw: `{"dependencies":[{"name":"cwe","version":"1.2.3","version":"2.3.4"}]}`, wantErr: "duplicate key"},
		{name: "duplicate unselected metadata key", raw: "dependencies: [{name: cwe, version: 1.2.3}]\nmetadata: {x: 1, x: 2}", wantErr: "duplicate key"},
		{name: "alias", raw: "item: &item {name: cwe, version: 1.2.3}\ndependencies: [*item]", wantErr: "anchors or aliases"},
		{name: "anchor alone", raw: "dependencies: [{name: cwe, version: &version 1.2.3}]", wantErr: "anchors or aliases"},
		{name: "merge key", raw: "dependencies: [{name: cwe, version: 1.2.3, <<: {extra: value}}]", wantErr: "string object keys"},
		{name: "numeric mapping key", raw: "dependencies: [{name: cwe, version: 1.2.3}]\n1: metadata", wantErr: "string object keys"},
		{name: "custom scalar tag", raw: "dependencies: [{name: cwe, version: !custom 1.2.3}]", wantErr: "unsupported scalar tag"},
		{name: "custom mapping tag", raw: "dependencies: [!custom {name: cwe, version: 1.2.3}]", wantErr: "unsupported mapping tag"},
		{name: "custom sequence tag", raw: "dependencies: !custom [{name: cwe, version: 1.2.3}]", wantErr: "unsupported sequence tag"},
		{name: "extra document", raw: "dependencies: [{name: cwe, version: 1.2.3}]\n---\nother: value", wantErr: "exactly one"},
		{name: "extra empty document", raw: "dependencies: [{name: cwe, version: 1.2.3}]\n---\n", wantErr: "exactly one"},
		{name: "malformed YAML", raw: "dependencies: [", wantErr: "parsing"},
		{name: "empty document", raw: "", wantErr: "must contain"},
		{name: "sequence root", raw: "[]", wantErr: "must be an object"},
		{name: "selector on mapping", raw: "dependencies: {cwe: {version: 1.2.3}}", wantErr: "requires a sequence"},
		{name: "missing collection", raw: "other: value", wantErr: "missing object field"},
		{
			name:    "inconsistent collection identity keys",
			raw:     "dependencies: [{name: cwe, id: cwe, version: 1.2.3}, {name: spire, id: spire, version: 2.3.4}]",
			paths:   map[string]string{"cwe": "dependencies[name=cwe].version", "spire": "dependencies[id=spire].version"},
			wantErr: "inconsistent identity keys",
		},
		{
			name:    "unknown nested member",
			raw:     "groups: [{name: controllers, components: [{name: cwe, version: 1.2.3}, {name: hidden, version: 2.3.4}]}]",
			paths:   map[string]string{"cwe": "groups[name=controllers].components[name=cwe].version"},
			wantErr: "unaccounted member",
		},
		{
			name:    "unknown outer member",
			raw:     "groups: [{name: controllers, components: [{name: cwe, version: 1.2.3}]}, {name: hidden, components: []}]",
			paths:   map[string]string{"cwe": "groups[name=controllers].components[name=cwe].version"},
			wantErr: "unaccounted member",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.paths == nil {
				tt.paths = map[string]string{"cwe": "dependencies[name=cwe].version"}
			}
			members := make(map[string]PinSetMember, len(tt.paths))
			for name, path := range tt.paths {
				members[name] = PinSetMember{Source: name, Path: path}
			}
			got, err := extractPinSetVersions([]byte(tt.raw), members)
			if tt.wantErr != "" {
				if codeOf(err) != e.CodeInvalidCatalog || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want coded invalid catalog containing %q, got %v", tt.wantErr, err)
				}
				if got != nil {
					t.Fatalf("failure returned partial versions: %#v", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestPinSetExtractionResourceBounds(t *testing.T) {
	base := map[string]PinSetMember{"cwe": {Source: "cwe", Path: "version"}}
	tests := []struct {
		name    string
		raw     string
		members map[string]PinSetMember
	}{
		{name: "file size", raw: strings.Repeat(" ", int(MaxCatalogFileBytes)+1), members: base},
		{name: "depth", raw: "version: 1.2.3\nmetadata: " + strings.Repeat("[", maxPinSetDocumentDepth+1) + "x" + strings.Repeat("]", maxPinSetDocumentDepth+1), members: base},
		{name: "node count", raw: "version: 1.2.3\nmetadata: [" + strings.Repeat("x,", maxPinSetDocumentNodes) + "x]", members: base},
		{name: "empty member set", raw: "version: 1.2.3"},
	}
	tooMany := make(map[string]PinSetMember)
	for index := 0; index <= maxPinSetMembers; index++ {
		tooMany[fmt.Sprintf("member-%d", index)] = base["cwe"]
	}
	tests = append(tests, struct {
		name    string
		raw     string
		members map[string]PinSetMember
	}{name: "member count", raw: "version: 1.2.3", members: tooMany})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractPinSetVersions([]byte(tt.raw), tt.members)
			if got != nil || codeOf(err) != e.CodeInvalidCatalog {
				t.Fatalf("resource limit must fail without partial results: %v, %v", got, err)
			}
		})
	}
}

func TestValidatePinSetValuePath(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{"version", true},
		{"metadata.release.version", true},
		{"dependencies[name=cwe].version", true},
		{"components[bom-ref=urn:uuid:1234].version", true},
		{"components[bom-ref=pkg:helm/spire@1.2.3].version", true},
		{"groups[name=controller].components[name=cwe].version", true},
		{"", false},
		{".version", false},
		{"version.", false},
		{"a..version", false},
		{"version ", false},
		{"version\n", false},
		{"a[0].version", false},
		{"a[*].version", false},
		{"a[name=*].version", false},
		{"a[name=x", false},
		{"a[name=x]version", false},
		{"a[name=x][other=y].version", false},
		{"a[name='x'].version", false},
		{"a[ name=x].version", false},
		{"a[name=x y].version", false},
		{"a[name=x==y].version", false},
		{"a[name=x || y].version", false},
		{"a[name=$(touch)].version", false},
		{"a[name={{x}}].version", false},
		{"$.a.version", false},
		{strings.Repeat("a.", maxPinSetPathSegments) + "a", false},
		{strings.Repeat("a", maxPinSetPathBytes+1), false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validatePinSetValuePath(tt.path)
			if tt.ok && err != nil {
				t.Fatal(err)
			}
			if !tt.ok && codeOf(err) != e.CodeInvalidCatalog {
				t.Fatalf("want invalid catalog, got %v", err)
			}
		})
	}
}

func TestExactPinSetVersion(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"1.2.3", true}, {"v1.2.3", true}, {"0.0.0", true},
		{"1.2.3-rc.1", true}, {"1.2.3-0", true}, {"1.2.3-01a", true},
		{"1.2.3+build.001", true}, {"1.2.3-rc.1+build.3", true},
		{"", false}, {"1.2", false}, {"1", false}, {"latest", false},
		{"1.2.x", false}, {"^1.2.3", false}, {">=1.2.3", false},
		{"1.2.3 - 2.0.0", false}, {"1.2.3 || 2.0.0", false},
		{" 1.2.3", false}, {"1.2.3\n", false}, {"01.2.3", false},
		{"1.2.3-01", false}, {"1.2.3-rc.01", false}, {"1.2.3-", false},
		{"1.2.3+", false}, {"1.2.3-rc..1", false}, {"1.2.3+build..1", false},
		{"1.2.3+" + strings.Repeat("a", maxPinSetVersionBytes), false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := exactPinSetVersion(tt.value); got != tt.want {
				t.Fatalf("exactPinSetVersion(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
