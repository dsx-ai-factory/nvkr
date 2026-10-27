// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeAccountHomeExcludesAmbientSelection(t *testing.T) {
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{"", "/synthetic/foreign-account"} {
		t.Run("HOME="+home, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("USER", "synthetic-foreign-user")
			t.Setenv("XDG_CACHE_HOME", "/synthetic/foreign-cache")
			if home == "" {
				if err := os.Unsetenv("HOME"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := nativeAccountHome(t.Context())
			if err != nil || got != account.HomeDir {
				t.Fatalf("account home=%q error=%v; want actual UID account home", got, err)
			}
			if os.Getenv("HOME") != home || os.Getenv("USER") != "synthetic-foreign-user" || os.Getenv("XDG_CACHE_HOME") != "/synthetic/foreign-cache" {
				t.Fatal("account lookup changed the caller environment")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var coded *nvkrerr.Error
	if _, err := nativeAccountHome(ctx); !errors.As(err, &coded) || coded.Code != nvkrerr.CodeTimeout {
		t.Fatalf("canceled account admission lost coded timeout: %v", err)
	}
}

func TestDaemonEnvironmentBindings(t *testing.T) {
	bound := map[string]string{"HOME": "/account", "XDG_RUNTIME_DIR": "/run/user/501"}
	for _, test := range []struct {
		name, raw string
		fail      bool
	}{{"native", "HOME=/account\x00XDG_RUNTIME_DIR=/run/user/501\x00", false}, {"defaults", "HOME=/account\x00SECRET=hidden\x00", false}, {"foreign", "HOME=/foreign\x00", true}, {"duplicate", "HOME=/account\x00HOME=/account\x00", true}, {"oversize", strings.Repeat("x", (1<<20)+1), true}} {
		t.Run(test.name, func(t *testing.T) {
			err := checkDaemonBindings([]byte(test.raw), bound)
			if (err != nil) != test.fail {
				t.Fatalf("error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "hidden") {
				t.Fatal("unrelated credential leaked")
			}
		})
	}
}
func TestExactSeedProjection(t *testing.T) {
	block := []byte(fmt.Sprintf("  - id: %q\n    name: \"cpu4-mem6gb-disk60gb\"\n    site_id: %q\n    gpu_count: 0\n    cpu_count: 4\n    memory_gib: 6\n    disk_gib: 60\n", TenantInstanceType, SiteID))
	raw := append(append([]byte("# prefix unchanged\n"), block...), []byte("# suffix unchanged\n")...)
	before := bytes.Clone(raw)
	projected, err := projectSeedFixture(raw, plainSHA(raw))
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.Replace(raw, []byte("cpu4-mem6gb"), []byte("cpu6-mem12gb"), 1)
	expected = bytes.Replace(expected, []byte("cpu_count: 4"), []byte("cpu_count: 6"), 1)
	expected = bytes.Replace(expected, []byte("memory_gib: 6"), []byte("memory_gib: 12"), 1)
	if !bytes.Equal(projected, expected) || !bytes.Equal(raw, before) {
		t.Fatal("projection changed nonselected bytes or source")
	}
	for _, test := range []struct {
		name   string
		data   []byte
		digest string
	}{{"changed", raw, strings.Repeat("f", 64)}, {"duplicate", append(bytes.Clone(raw), block...), plainSHA(append(bytes.Clone(raw), block...))}, {"missing", []byte("missing"), plainSHA([]byte("missing"))}, {"oversize", append(bytes.Clone(raw), make([]byte, 65536)...), plainSHA(append(bytes.Clone(raw), make([]byte, 65536)...))}} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := projectSeedFixture(test.data, test.digest); err == nil {
				t.Fatal("unsafe fixture accepted")
			}
		})
	}
}

// The optional nightly source suite retains its exact source-fixture guard.
const SeedDigest = "e713b6e74e884bfba5a942b10eee142171565dcec2f24c6217cd9baecea03cac"

type seedProjection struct {
	SourceDigest  string `json:"sourceDigest"`
	RuntimeDigest string `json:"runtimeDigest"`
	InstanceType  string `json:"instanceType"`
	VCPUs         int    `json:"vcpus"`
	MemoryGiB     int    `json:"memoryGiB"`
	DiskGiB       int    `json:"diskGiB"`
}

func projectSeed(ctx context.Context, work string) (seedProjection, error) {
	var result seedProjection
	if err := requireContext(ctx); err != nil {
		return result, err
	}
	file, info, err := securepath.OpenRegular(filepath.Join(work, "internal/seed/default-seed.yaml"))
	if err != nil {
		return result, err
	}
	defer file.Close()
	if !currentOwner(info) || info.Size() > 65536 {
		return result, errors.New("source seed is not a bounded regular owned file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return result, err
	}
	projected, err := projectSeedBytes(raw)
	if err != nil {
		return result, err
	}
	if err = privateWrite(filepath.Join(work, "laptop-seed.yaml"), projected); err != nil {
		return result, err
	}
	return seedProjection{"sha256:" + SeedDigest, "sha256:" + plainSHA(projected), TenantInstanceType, tenantDemoVCPUs, tenantDemoMemoryGiB, 60}, nil
}
func projectSeedBytes(raw []byte) ([]byte, error) { return projectSeedFixture(raw, SeedDigest) }
func projectSeedFixture(raw []byte, digest string) ([]byte, error) {
	block := []byte(fmt.Sprintf("  - id: %q\n    name: \"cpu4-mem6gb-disk60gb\"\n    site_id: %q\n    gpu_count: 0\n    cpu_count: 4\n    memory_gib: 6\n    disk_gib: 60\n", TenantInstanceType, SiteID))
	// This is an exact-byte fixture projection, not a YAML parser. NICoSim owns
	// decoding; accepted source assets and their identity remain untouched.
	if len(raw) > 65536 || plainSHA(raw) != digest || bytes.Count(raw, block) != 1 {
		return nil, errors.New("source simulator seed differs from inspected laptop interface")
	}
	replacement := bytes.ReplaceAll(block, []byte("cpu4"), fmt.Appendf(nil, "cpu%d", tenantDemoVCPUs))
	replacement = bytes.ReplaceAll(replacement, []byte("cpu_count: 4"), fmt.Appendf(nil, "cpu_count: %d", tenantDemoVCPUs))
	replacement = bytes.ReplaceAll(replacement, []byte("mem6gb"), fmt.Appendf(nil, "mem%dgb", tenantDemoMemoryGiB))
	replacement = bytes.ReplaceAll(replacement, []byte("memory_gib: 6"), fmt.Appendf(nil, "memory_gib: %d", tenantDemoMemoryGiB))
	return bytes.Replace(raw, block, replacement, 1), nil
}
