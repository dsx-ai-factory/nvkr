package catalog

import (
	"bytes"
	"io"
	"regexp"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"go.yaml.in/yaml/v3"
)

const (
	maxPinSetDocumentDepth = 64
	maxPinSetDocumentNodes = 65536
	maxPinSetMembers       = 1024
	maxPinSetPathBytes     = 4096
	maxPinSetPathSegments  = 32
	maxPinSetVersionBytes  = 256
)

var (
	pinSetFieldPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	pinSetValuePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+%-]*$`)
	pinSetVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
)

type pinSetPathSegment struct {
	field string
	key   string
	value string
}

// pinSetCollection records the complete membership of one selector-addressed
// sequence. Multiple paths through that sequence must use the same identity key.
type pinSetCollection struct {
	path      string
	key       string
	items     map[string]*yaml.Node
	accounted map[string]bool
}

// extractPinSetVersions interprets only object fields and literal equality
// selectors. It has no I/O or expression evaluation. Selector-addressed sequences
// have exact membership; unrelated object fields may carry upstream metadata.
func extractPinSetVersions(raw []byte, members map[string]PinSetMember) (map[string]string, error) {
	if len(members) == 0 || len(members) > maxPinSetMembers {
		return nil, e.New(e.CodeInvalidCatalog, "pin set must declare between 1 and %d members", maxPinSetMembers)
	}
	paths := make(map[string][]pinSetPathSegment, len(members))
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path, err := parsePinSetValuePath(members[name].Path)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "pin set member %q", name)
		}
		paths[name] = path
	}
	root, err := decodePinSetDocument(raw)
	if err != nil {
		return nil, err
	}
	collections := make(map[*yaml.Node]*pinSetCollection)
	versions := make(map[string]string, len(members))
	for _, name := range names {
		value, err := extractPinSetValue(root, paths[name], collections)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "pin set member %q at %q", name, members[name].Path)
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || !exactPinSetVersion(value.Value) {
			return nil, e.New(e.CodeInvalidCatalog, "pin set member %q at %q must contain an exact string version", name, members[name].Path)
		}
		versions[name] = value.Value
	}
	// Sort diagnostics independently of Go's map iteration order.
	var unknown []string
	for _, collection := range collections {
		for name := range collection.items {
			if !collection.accounted[name] {
				unknown = append(unknown, collection.path+"["+collection.key+"="+name+"]")
			}
		}
	}
	if len(unknown) != 0 {
		sort.Strings(unknown)
		return nil, e.New(e.CodeInvalidCatalog, "pin set has unaccounted member %q; declare every member of each selected collection", unknown[0])
	}
	return versions, nil
}

func validatePinSetValuePath(path string) error {
	_, err := parsePinSetValuePath(path)
	return err
}

func parsePinSetValuePath(path string) ([]pinSetPathSegment, error) {
	invalid := func() ([]pinSetPathSegment, error) {
		return nil, e.New(e.CodeInvalidCatalog, "invalid pin set value path %q; use dotted fields and literal [key=value] selectors", path)
	}
	if len(path) == 0 || len(path) > maxPinSetPathBytes {
		return invalid()
	}
	var segments []pinSetPathSegment
	for rest := path; rest != ""; {
		end := strings.IndexAny(rest, ".[ ")
		if end == -1 {
			end = len(rest)
		}
		segment := pinSetPathSegment{field: rest[:end]}
		if !pinSetFieldPattern.MatchString(segment.field) {
			return invalid()
		}
		rest = rest[end:]
		if strings.HasPrefix(rest, "[") {
			closeIndex := strings.IndexByte(rest, ']')
			if closeIndex < 0 {
				return invalid()
			}
			key, value, ok := strings.Cut(rest[1:closeIndex], "=")
			if !ok || !pinSetFieldPattern.MatchString(key) || !pinSetValuePattern.MatchString(value) {
				return invalid()
			}
			segment.key, segment.value = key, value
			rest = rest[closeIndex+1:]
		}
		segments = append(segments, segment)
		if len(segments) > maxPinSetPathSegments {
			return invalid()
		}
		if rest == "" {
			break
		}
		if rest[0] != '.' || len(rest) == 1 {
			return invalid()
		}
		rest = rest[1:]
	}
	return segments, nil
}

func exactPinSetVersion(version string) bool {
	if len(version) > maxPinSetVersionBytes {
		return false
	}
	parts := pinSetVersionPattern.FindStringSubmatch(version)
	if parts == nil {
		return false
	}
	for _, identifier := range strings.Split(parts[4], ".") {
		if len(identifier) > 1 && identifier[0] == '0' && strings.Trim(identifier, "0123456789") == "" {
			return false
		}
	}
	return true
}

func decodePinSetDocument(raw []byte) (*yaml.Node, error) {
	if len(raw) == 0 || int64(len(raw)) > MaxCatalogFileBytes {
		return nil, e.New(e.CodeInvalidCatalog, "pin set document must contain 1 to %d bytes", MaxCatalogFileBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing pin set document")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, e.New(e.CodeInvalidCatalog, "pin set document must be an object")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, e.New(e.CodeInvalidCatalog, "pin set must contain exactly one YAML or JSON document")
	}
	count := 0
	if err := validatePinSetNode(document.Content[0], 0, &count); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}

func validatePinSetNode(node *yaml.Node, depth int, count *int) error {
	*count++
	if depth > maxPinSetDocumentDepth || *count > maxPinSetDocumentNodes {
		return e.New(e.CodeInvalidCatalog, "pin set document exceeds depth or node limit")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return e.New(e.CodeInvalidCatalog, "pin set document cannot contain YAML anchors or aliases")
	}
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return e.New(e.CodeInvalidCatalog, "pin set document has unsupported mapping tag %q", node.Tag)
		}
		seen := make(map[string]bool, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return e.New(e.CodeInvalidCatalog, "pin set document requires string object keys")
			}
			if seen[key.Value] {
				return e.New(e.CodeInvalidCatalog, "pin set document has duplicate key %q", key.Value)
			}
			seen[key.Value] = true
		}
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return e.New(e.CodeInvalidCatalog, "pin set document has unsupported sequence tag %q", node.Tag)
		}
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str", "!!bool", "!!int", "!!float", "!!null", "!!timestamp":
		default:
			return e.New(e.CodeInvalidCatalog, "pin set document has unsupported scalar tag %q", node.Tag)
		}
	default:
		return e.New(e.CodeInvalidCatalog, "pin set document has unsupported YAML node")
	}
	for _, child := range node.Content {
		if err := validatePinSetNode(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func extractPinSetValue(root *yaml.Node, path []pinSetPathSegment, collections map[*yaml.Node]*pinSetCollection) (*yaml.Node, error) {
	current := root
	var traversed string
	for _, segment := range path {
		if traversed != "" {
			traversed += "."
		}
		traversed += segment.field
		field := pinSetObjectField(current, segment.field)
		if field == nil {
			return nil, e.New(e.CodeInvalidCatalog, "missing object field %q", traversed)
		}
		current = field
		if segment.key == "" {
			continue
		}
		if current.Kind != yaml.SequenceNode {
			return nil, e.New(e.CodeInvalidCatalog, "selector at %q requires a sequence", traversed)
		}
		collection := collections[current]
		if collection == nil {
			var err error
			collection, err = readPinSetCollection(current, traversed, segment.key)
			if err != nil {
				return nil, err
			}
			collections[current] = collection
		} else if collection.key != segment.key {
			return nil, e.New(e.CodeInvalidCatalog, "selectors at %q use inconsistent identity keys %q and %q", traversed, collection.key, segment.key)
		}
		current = collection.items[segment.value]
		if current == nil {
			return nil, e.New(e.CodeInvalidCatalog, "selector at %q has no member %q", traversed, segment.value)
		}
		collection.accounted[segment.value] = true
		traversed += "[" + segment.key + "=" + segment.value + "]"
	}
	return current, nil
}

func readPinSetCollection(node *yaml.Node, path, key string) (*pinSetCollection, error) {
	collection := &pinSetCollection{path: path, key: key, items: make(map[string]*yaml.Node), accounted: make(map[string]bool)}
	for _, item := range node.Content {
		identity := pinSetObjectField(item, key)
		if identity == nil || identity.Kind != yaml.ScalarNode || identity.Tag != "!!str" || identity.Value == "" || strings.TrimSpace(identity.Value) != identity.Value {
			return nil, e.New(e.CodeInvalidCatalog, "each member at %q requires a nonempty string identity %q without surrounding whitespace", path, key)
		}
		if collection.items[identity.Value] != nil {
			return nil, e.New(e.CodeInvalidCatalog, "collection at %q has duplicate %s %q", path, key, identity.Value)
		}
		collection.items[identity.Value] = item
	}
	return collection, nil
}

func pinSetObjectField(node *yaml.Node, field string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == field {
			return node.Content[index+1]
		}
	}
	return nil
}
