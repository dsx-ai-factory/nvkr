package releaseasset

import "testing"

func TestToolLimitsPolicy(t *testing.T) {
	want := Limits{ExecutableBytes: 256 << 20, ExpandedBytes: 1 << 30, Entries: 4096}
	if got := ToolLimits(); got != want || MaxToolTransportBytes != 256<<20 {
		t.Fatalf("shared tool acquisition limits changed: %+v", got)
	}
	copy := ToolLimits()
	copy.Entries = 1
	if ToolLimits() != want {
		t.Fatal("caller mutation changed the shared tool policy")
	}
}
