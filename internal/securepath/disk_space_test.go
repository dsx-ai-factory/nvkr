package securepath

import (
	"errors"
	"math"
	"testing"
)

func TestDiskBackedCapacityObservation(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		facts diskSpaceFacts
		err   error
		want  uint64
	}{
		{name: "available bytes", facts: diskSpaceFacts{blockSize: 4096, available: 10}, want: 40960},
		{name: "zero available bytes", facts: diskSpaceFacts{blockSize: 4096}},
		{name: "read only", facts: diskSpaceFacts{blockSize: 4096, available: 10, readOnly: true}, err: errors.New("read-only")},
		{name: "memory backed", facts: diskSpaceFacts{blockSize: 4096, available: math.MaxUint64, memoryBacked: true}, err: errors.New("memory-backed")},
		{name: "invalid block size", facts: diskSpaceFacts{available: 10}, err: errors.New("invalid")},
		{name: "overflow", facts: diskSpaceFacts{blockSize: 4096, available: math.MaxUint64}, err: errors.New("overflow")},
		{name: "observation failure", err: errors.New("injected syscall failure")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory, _, err := OpenDirectory(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			got, err := availableDiskBackedBytes(t.Context(), directory, func(fd int) (diskSpaceFacts, error) {
				if fd != int(directory.Fd()) {
					t.Fatal("observation did not use held directory")
				}
				if scenario.name == "observation failure" {
					return diskSpaceFacts{}, scenario.err
				}
				return scenario.facts, nil
			})
			if (err != nil) != (scenario.err != nil) || got != scenario.want {
				t.Fatalf("capacity=%d, err=%v; want=%d, failure=%t", got, err, scenario.want, scenario.err != nil)
			}
		})
	}
}
