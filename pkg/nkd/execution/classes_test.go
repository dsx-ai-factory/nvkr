package execution

import (
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func testClass(name string, platform HostPlatform, kvm bool) Class {
	class := Class{
		Name: name, RunsOn: name, HostPlatform: platform,
		Resources: ResourceCapacity{
			CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536,
		},
		MaxTimeoutSeconds: 14400,
	}
	if kvm {
		class.Capabilities = []CapabilityName{CapabilityKVM}
	}
	return class
}

func testRequirements(policy CapabilityPolicy) *Requirements {
	return &Requirements{
		Resources: &ResourceRequest{
			CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768,
		},
		AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64, HostPlatformLinuxARM64},
		Capabilities:         []CapabilityRequirement{{Name: CapabilityKVM, Policy: policy}},
		TimeoutSeconds:       7200,
	}
}

func TestValidateClasses(t *testing.T) {
	for _, test := range []struct {
		name    string
		classes []Class
		code    nkderr.Code
	}{
		{name: "valid", classes: []Class{testClass("a", HostPlatformLinuxAMD64, false), testClass("b", HostPlatformLinuxARM64, true)}},
		{name: "omitted"},
		{name: "declared empty", classes: []Class{}, code: nkderr.CodeInvalidCatalog},
		{name: "noncanonical name", classes: []Class{testClass("A", HostPlatformLinuxAMD64, false)}, code: nkderr.CodeInvalidCatalog},
		{name: "unsafe route", classes: []Class{func() Class { c := testClass("a", HostPlatformLinuxAMD64, false); c.RunsOn = "bad\nroute"; return c }()}, code: nkderr.CodeInvalidCatalog},
		{name: "unknown platform", classes: []Class{testClass("a", "linux/riscv64", false)}, code: nkderr.CodeInvalidCatalog},
		{name: "missing timeout", classes: []Class{func() Class { c := testClass("a", HostPlatformLinuxAMD64, false); c.MaxTimeoutSeconds = 0; return c }()}, code: nkderr.CodeInvalidCatalog},
		{name: "timeout overflow", classes: []Class{func() Class {
			c := testClass("a", HostPlatformLinuxAMD64, false)
			c.MaxTimeoutSeconds = maxTimeoutSeconds + 1
			return c
		}()}, code: nkderr.CodeInvalidCatalog},
		{name: "unknown capability", classes: []Class{func() Class {
			c := testClass("a", HostPlatformLinuxAMD64, false)
			c.Capabilities = []CapabilityName{"gpu"}
			return c
		}()}, code: nkderr.CodeInvalidCatalog},
		{name: "duplicate capability", classes: []Class{func() Class {
			c := testClass("a", HostPlatformLinuxAMD64, true)
			c.Capabilities = append(c.Capabilities, CapabilityKVM)
			return c
		}()}, code: nkderr.CodeInvalidCatalog},
		{name: "unsorted classes", classes: []Class{testClass("b", HostPlatformLinuxAMD64, false), testClass("a", HostPlatformLinuxAMD64, false)}, code: nkderr.CodeInvalidCatalog},
		{name: "duplicate classes", classes: []Class{testClass("a", HostPlatformLinuxAMD64, false), testClass("a", HostPlatformLinuxAMD64, false)}, code: nkderr.CodeInvalidCatalog},
		{name: "duplicate route", classes: []Class{testClass("a", HostPlatformLinuxAMD64, false), func() Class {
			c := testClass("b", HostPlatformLinuxARM64, true)
			c.RunsOn = "a"
			return c
		}()}, code: nkderr.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateClasses(test.classes)
			if test.code == "" && err != nil {
				t.Fatalf("valid classes rejected: %v", err)
			}
			if test.code != "" && errorCode(err) != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestMatchExecutionClass(t *testing.T) {
	classes := []Class{
		testClass("amd64-tcg", HostPlatformLinuxAMD64, false),
		testClass("arm64-kvm", HostPlatformLinuxARM64, true),
		testClass("z-amd64-kvm", HostPlatformLinuxAMD64, true),
	}
	for _, test := range []struct {
		name        string
		request     *Requirements
		options     MatchOptions
		wantClass   string
		wantKVM     bool
		wantCode    nkderr.Code
		editClasses func([]Class) []Class
	}{
		{name: "nil request needs no class", request: nil},
		{name: "required capability", request: testRequirements(CapabilityRequired), wantClass: "arm64-kvm", wantKVM: true},
		{name: "preferred chooses capability", request: testRequirements(CapabilityPreferred), wantClass: "arm64-kvm", wantKVM: true},
		{name: "preferred falls back without capability", request: testRequirements(CapabilityPreferred), wantClass: "amd64-tcg", editClasses: func(classes []Class) []Class { return classes[:1] }},
		{name: "forbidden chooses unexposed", request: testRequirements(CapabilityForbidden), wantClass: "amd64-tcg"},
		{name: "hard host constraint", request: testRequirements(CapabilityRequired), options: MatchOptions{RequiredHostPlatform: HostPlatformLinuxAMD64}, wantClass: "z-amd64-kvm", wantKVM: true},
		{name: "capacity unavailable", request: func() *Requirements {
			r := testRequirements(CapabilityPreferred)
			r.Resources.MemoryMiB = 32768
			return r
		}(), wantCode: nkderr.CodeEngineCapability},
		{name: "timeout unavailable", request: func() *Requirements { r := testRequirements(CapabilityPreferred); r.TimeoutSeconds = 28800; return r }(), wantCode: nkderr.CodeEngineCapability},
		{name: "required capability unavailable", request: testRequirements(CapabilityRequired), wantCode: nkderr.CodeEngineCapability, editClasses: func(classes []Class) []Class { return classes[:1] }},
		{name: "class inventory omitted", request: testRequirements(CapabilityPreferred), wantCode: nkderr.CodeEngineCapability, editClasses: func([]Class) []Class { return nil }},
		{name: "required platform unavailable", request: testRequirements(CapabilityPreferred), options: MatchOptions{RequiredHostPlatform: HostPlatformLinuxARM64}, wantCode: nkderr.CodeEngineCapability, editClasses: func(classes []Class) []Class { return classes[:1] }},
		{name: "unsupported required platform", request: testRequirements(CapabilityPreferred), options: MatchOptions{RequiredHostPlatform: "linux/riscv64"}, wantCode: nkderr.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := append([]Class(nil), classes...)
			if test.editClasses != nil {
				input = test.editClasses(input)
			}
			binding, err := Match(test.request, input, test.options)
			if test.wantCode != "" {
				if errorCode(err) != test.wantCode || binding != nil {
					t.Fatalf("binding=%#v error=%v, want %s", binding, err, test.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.request == nil {
				if binding != nil {
					t.Fatalf("nil request selected %#v", binding)
				}
				return
			}
			if binding.Class.Name != test.wantClass || len(binding.Capabilities) != 1 || binding.Capabilities[0].Enabled != test.wantKVM {
				t.Fatalf("binding = %#v, want class=%q kvm=%t", binding, test.wantClass, test.wantKVM)
			}
			if err := binding.ValidateFor(test.request); err != nil {
				t.Fatalf("selected binding did not validate: %v", err)
			}
		})
	}
}

func TestMatchTieBreaksByCanonicalName(t *testing.T) {
	request := testRequirements(CapabilityPreferred)
	classes := []Class{
		testClass("a-kvm", HostPlatformLinuxAMD64, true),
		testClass("b-kvm", HostPlatformLinuxARM64, true),
	}
	binding, err := Match(request, classes, MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Class.Name != "a-kvm" {
		t.Fatalf("tie selected %q, want a-kvm", binding.Class.Name)
	}
}

func TestBindingValidationRejectsSubstitution(t *testing.T) {
	request := testRequirements(CapabilityPreferred)
	binding, err := Match(request, []Class{testClass("kvm", HostPlatformLinuxAMD64, true)}, MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		edit     func(*Binding)
		wantCode nkderr.Code
	}{
		{name: "missing resolution", edit: func(binding *Binding) { binding.Capabilities = nil }, wantCode: nkderr.CodeInvalidCatalog},
		{name: "changed resolution", edit: func(binding *Binding) { binding.Capabilities[0].Enabled = false }, wantCode: nkderr.CodeInvalidCatalog},
		{name: "insufficient memory", edit: func(binding *Binding) { binding.Class.Resources.MemoryMiB = 1 }, wantCode: nkderr.CodeEngineCapability},
		{name: "insufficient timeout", edit: func(binding *Binding) { binding.Class.MaxTimeoutSeconds = 1 }, wantCode: nkderr.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := CloneBinding(binding)
			test.edit(changed)
			if err := changed.ValidateFor(request); errorCode(err) != test.wantCode {
				t.Fatalf("error = %v, want %s", err, test.wantCode)
			}
		})
	}
}

func TestBindingValidateForGuards(t *testing.T) {
	for _, test := range []struct {
		name         string
		binding      *Binding
		requirements *Requirements
		wantCode     nkderr.Code
	}{
		{
			name:         "nil binding with requirements",
			requirements: testRequirements(CapabilityPreferred),
			wantCode:     nkderr.CodeEngineCapability,
		},
		{
			name:     "binding with nil requirements",
			binding:  &Binding{Class: testClass("tcg", HostPlatformLinuxAMD64, false)},
			wantCode: nkderr.CodeInvalidCatalog,
		},
		{
			name: "required capability absent",
			binding: &Binding{
				Class:        testClass("tcg", HostPlatformLinuxAMD64, false),
				Capabilities: []CapabilityResolution{{Name: CapabilityKVM, Enabled: false}},
			},
			requirements: testRequirements(CapabilityRequired),
			wantCode:     nkderr.CodeEngineCapability,
		},
		{
			name: "forbidden capability exposed",
			binding: &Binding{
				Class:        testClass("kvm", HostPlatformLinuxAMD64, true),
				Capabilities: []CapabilityResolution{{Name: CapabilityKVM, Enabled: true}},
			},
			requirements: testRequirements(CapabilityForbidden),
			wantCode:     nkderr.CodeEngineCapability,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.binding.ValidateFor(test.requirements); errorCode(err) != test.wantCode {
				t.Fatalf("error = %v, want %s", err, test.wantCode)
			}
		})
	}
}

func errorCode(err error) nkderr.Code {
	if coded, ok := err.(*nkderr.Error); ok {
		return coded.Code
	}
	return ""
}
