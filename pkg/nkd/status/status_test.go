package status

import (
	"errors"
	"strings"
	"testing"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

const target = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const replacement = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func code(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func declaration(action string) StatusDeclaration {
	return StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: action}
}

func TestDeclarationValidationIncludesSchemaActionAndTime(t *testing.T) {
	tests := []struct {
		name string
		decl StatusDeclaration
		code nkderr.Code
	}{
		{name: "schema", decl: StatusDeclaration{Target: target, Action: ActionWithdrawn}, code: nkderr.CodeInvalidCatalog},
		{name: "target", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: "not-digest", Action: ActionWithdrawn}, code: nkderr.CodeUnverifiedRef},
		{name: "action", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: "bogus"}, code: nkderr.CodeInvalidCatalog},
		{name: "superseded target", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: ActionSuperseded}, code: nkderr.CodeInvalidCatalog},
		{name: "negative time", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: ActionWithdrawn, EffectiveEpoch: -1}, code: nkderr.CodeInvalidCatalog},
		{name: "extraneous replacement", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: ActionWithdrawn, ReplacedBy: replacement}, code: nkderr.CodeInvalidCatalog},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := code(test.decl.Validate()); got != test.code {
				t.Fatalf("got %s, want %s", got, test.code)
			}
		})
	}
	valid := declaration(ActionSuperseded)
	valid.ReplacedBy = replacement
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid declaration rejected: %v", err)
	}
}

func TestExplicitAsOfAndReason(t *testing.T) {
	if _, err := At(0); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("zero explicit as-of must be rejected: %v", err)
	}
	if !strings.Contains(reason("eol"), "eol") {
		t.Fatal("reason rendering lost text")
	}
}
