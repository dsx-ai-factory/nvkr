package status

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCheckpointInvalidShapeHasNoSigningBytesOrIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*StatusCheckpoint)
		code e.Code
	}{
		{"schema", func(c *StatusCheckpoint) { c.SchemaVersion = "unknown" }, e.CodeInvalidCatalog},
		{"target", func(c *StatusCheckpoint) { c.Target = "latest" }, e.CodeUnverifiedRef},
		{"authority", func(c *StatusCheckpoint) { c.Authority = " padded " }, e.CodeInvalidCatalog},
		{"genesis predecessor", func(c *StatusCheckpoint) { c.Sequence = 0 }, e.CodeStatusCheckpoint},
		{"missing predecessor", func(c *StatusCheckpoint) { c.PreviousCheckpoint = "" }, e.CodeStatusCheckpoint},
		{"invalid declaration pin", func(c *StatusCheckpoint) { c.DeclarationDigests = []string{"latest"} }, e.CodeUnverifiedRef},
		{"duplicate declaration", func(c *StatusCheckpoint) { c.DeclarationDigests = []string{statusDigest('a'), statusDigest('a')} }, e.CodeStatusCheckpoint},
		{"missing issue time", func(c *StatusCheckpoint) { c.IssuedEpoch = 0 }, e.CodeStatusCheckpoint},
		{"empty lifetime", func(c *StatusCheckpoint) { c.ValidUntilEpoch = c.IssuedEpoch }, e.CodeStatusCheckpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint := StatusCheckpoint{SchemaVersion: CheckpointSchemaVersion, Target: target, Authority: checkpointAuthority,
				Sequence: 1, PreviousCheckpoint: statusDigest('0'), DeclarationDigests: []string{}, IssuedEpoch: 1, ValidUntilEpoch: 2}
			tc.edit(&checkpoint)
			if err := checkpoint.Validate(); code(err) != tc.code {
				t.Fatalf("Validate = %v; want %s", err, tc.code)
			}
			if raw, err := checkpoint.Marshal(); code(err) != tc.code || len(raw) != 0 {
				t.Fatalf("Marshal = %q, %v", raw, err)
			}
			if digest, err := checkpoint.Digest(); code(err) != tc.code || digest != "" {
				t.Fatalf("Digest = %q, %v", digest, err)
			}
		})
	}
}

func TestCheckpointReceiptSeparatesBlobAndChainIdentities(t *testing.T) {
	request, checkpoint, _ := checkpointFixture(t)
	receipt, err := VerifyCheckpoint(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	blob, err := receipt.PayloadSHA256()
	if err != nil || blob != want {
		t.Fatalf("payload digest = %q, %v; want %q", blob, err, want)
	}
	chain, err := receipt.CheckpointDigest()
	if err != nil || chain == blob {
		t.Fatalf("chain identity must remain domain separated: %q, %v", chain, err)
	}
	signers, err := receipt.DeclarationSigners()
	if err != nil || !reflect.DeepEqual(signers, []string{checkpointSigner}) {
		t.Fatalf("signers = %v, %v", signers, err)
	}
	signers[0] = "substituted"
	again, err := receipt.DeclarationSigners()
	if err != nil || !reflect.DeepEqual(again, []string{checkpointSigner}) {
		t.Fatalf("caller changed receipt signers: %v, %v", again, err)
	}
	mark, err := receipt.HighWater()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		mark  HighWaterMark
		valid bool
	}{
		{"accepted", mark, true}, {"zero", HighWaterMark{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotTarget, targetErr := tc.mark.Target()
			gotAuthority, authorityErr := tc.mark.Authority()
			if tc.valid {
				if targetErr != nil || authorityErr != nil || gotTarget != target || gotAuthority != checkpointAuthority {
					t.Fatalf("high-water binding: %q/%q, %v/%v", gotTarget, gotAuthority, targetErr, authorityErr)
				}
			} else if gotTarget != "" || gotAuthority != "" || code(targetErr) != e.CodeStatusCheckpoint || code(authorityErr) != e.CodeStatusCheckpoint {
				t.Fatalf("zero mark exposed authority: %q/%q, %v/%v", gotTarget, gotAuthority, targetErr, authorityErr)
			}
		})
	}
	var zero CheckpointReceipt
	if got, err := zero.PayloadSHA256(); got != "" || code(err) != e.CodeStatusCheckpoint {
		t.Fatalf("zero receipt payload: %q, %v", got, err)
	}
	if got, err := zero.DeclarationSigners(); got != nil || code(err) != e.CodeStatusCheckpoint {
		t.Fatalf("zero receipt signers: %v, %v", got, err)
	}
}
