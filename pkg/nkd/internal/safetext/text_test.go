package safetext

import "testing"

func TestValidRejectsDisplayUnsafeUnicode(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "ordinary ASCII space", value: "two words", want: true},
		{name: "ordinary Unicode", value: "café", want: true},
		{name: "right-to-left override", value: "safe\u202eunsafe"},
		{name: "zero-width space", value: "safe\u200bunsafe"},
		{name: "zero-width joiner", value: "safe\u200dunsafe"},
		{name: "line separator", value: "safe\u2028unsafe"},
		{name: "paragraph separator", value: "safe\u2029unsafe"},
		{name: "no-break space", value: "safe\u00a0unsafe"},
		{name: "narrow no-break space", value: "safe\u202funsafe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Valid(test.value, 128, false); got != test.want {
				t.Fatalf("Valid(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}

func TestValidAppliesBoundsAndEmptyPolicy(t *testing.T) {
	if Valid("", 1, false) {
		t.Fatal("empty text passed a non-empty contract")
	}
	if !Valid("", 0, true) {
		t.Fatal("empty text failed an allow-empty contract")
	}
	if Valid("ab", 1, false) {
		t.Fatal("oversized text passed its byte bound")
	}
	if Valid(string([]byte{0xff}), 1, false) {
		t.Fatal("invalid UTF-8 passed validation")
	}
}
