// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"bytes"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	nativeyaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

func bootEnvironmentFixture() Environment {
	environment := objectEnvironment()
	environment.APIVersion = EnvironmentBootAPIVersion
	environment.Publication.Mode = "per-output-v1"
	selection := bootSelectionFixture()
	environment.Publication.BootSelection = &selection
	environment.Publication.ObjectStores[0].Mode = ObjectStoreBootMode
	environment.Publication.ObjectStores[0].Serving = &BootServing{
		Profile: BootServingProfile, Origin: "https://boot.example.test", Prefix: "/assets", ReaderMode: "anonymous",
	}
	return environment
}

func TestOldEnvironmentRejectsCaseVariantBootFieldsInYAML(t *testing.T) {
	raw, err := canonical.Marshal(objectEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ prefix, field string }{
		{`"publication":{`, "bootSelection"}, {`"publication":{`, "BootSelection"},
		{`"objectStores":[{`, "Mode"}, {`"objectStores":[{`, "Serving"},
	} {
		bad := bytes.Replace(raw, []byte(tc.prefix), []byte(tc.prefix+`"`+tc.field+`":null,`), 1)
		converted, err := yaml.JSONToYAML(bad)
		if err != nil {
			t.Fatal(err)
		}
		for _, document := range [][]byte{bad, converted} {
			_, err := DecodeEnvironment(document)
			requireCode(t, err, e.CodeInvalidCatalog)
		}
	}
}

func TestBootEnvironmentAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Environment)
		code e.Code
	}{
		{name: "selected destination"},
		{name: "additional explicit full mirror", edit: func(x *Environment) {
			full := x.Publication.ObjectStores[0]
			full.Name, full.Mode, full.Serving = "full", ObjectStoreFullMirrorMode, nil
			x.Publication.ObjectStores = append(x.Publication.ObjectStores, full)
		}},
		{name: "missing selection", edit: func(x *Environment) { x.Publication.BootSelection = nil }, code: e.CodeInvalidCatalog},
		{name: "empty selection", edit: func(x *Environment) { x.Publication.BootSelection.Disks = nil }, code: e.CodeInvalidCatalog},
		{name: "no destinations", edit: func(x *Environment) { x.Publication.ObjectStores = nil }, code: e.CodeInvalidCatalog},
		{name: "implicit mode", edit: func(x *Environment) { x.Publication.ObjectStores[0].Mode = "" }, code: e.CodeEngineCapability},
		{name: "unknown mode", edit: func(x *Environment) { x.Publication.ObjectStores[0].Mode = "best-effort" }, code: e.CodeEngineCapability},
		{name: "no selected destination", edit: func(x *Environment) {
			x.Publication.ObjectStores[0].Mode, x.Publication.ObjectStores[0].Serving = ObjectStoreFullMirrorMode, nil
		}, code: e.CodeInvalidCatalog},
		{name: "missing serving", edit: func(x *Environment) { x.Publication.ObjectStores[0].Serving = nil }, code: e.CodeInvalidCatalog},
		{name: "implicit reader mode", edit: func(x *Environment) { x.Publication.ObjectStores[0].Serving.ReaderMode = "" }, code: e.CodeEngineCapability},
		{name: "full mirror with serving", edit: func(x *Environment) { x.Publication.ObjectStores[0].Mode = ObjectStoreFullMirrorMode }, code: e.CodeInvalidCatalog},
		{name: "aggregate route", edit: func(x *Environment) { x.Publication.Mode = "" }, code: e.CodeEngineCapability},
		{name: "old schema", edit: func(x *Environment) { x.APIVersion = EnvironmentObjectStoresAPIVersion }, code: e.CodeInvalidCatalog},
		{name: "old schema with mode only", edit: func(x *Environment) {
			x.APIVersion, x.Publication.BootSelection = EnvironmentObjectStoresAPIVersion, nil
		}, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := bootEnvironmentFixture()
			if tc.edit != nil {
				tc.edit(&x)
			}
			err := x.Validate()
			if tc.code != "" {
				requireCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBootEnvironmentVersionedBytesAndOwnership(t *testing.T) {
	x := bootEnvironmentFixture()
	raw, err := canonical.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEnvironment(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := canonical.Marshal(decoded)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("versioned selection and serving bytes changed on roundtrip", err)
	}
	clone := x.Clone()
	clone.Publication.BootSelection.Disks[0].Output = "other-node"
	clone.Publication.BootSelection.Imagers[0].Profile = "other-profile"
	clone.Publication.ObjectStores[0].Serving.Origin = "https://other.example.test"
	after, err := canonical.Marshal(x)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("Environment clone aliases selection or serving", err)
	}
	beforeDigest, err := x.Digest()
	if err != nil {
		t.Fatal(err)
	}
	clone = x.Clone()
	clone.Publication.ObjectStores[0].Serving.Origin = "https://other.example.test"
	afterDigest, err := clone.Digest()
	if err != nil || beforeDigest == afterDigest {
		t.Fatal("serving mapping is not bound to Environment identity", err)
	}
}

func TestBootFieldsRefusedByOldEnvironmentSchemas(t *testing.T) {
	for _, version := range []string{EnvironmentAPIVersion, EnvironmentObjectStoresAPIVersion} {
		old := objectEnvironment()
		old.APIVersion = version
		if version == EnvironmentAPIVersion {
			old.Publication.ObjectStores = nil
		}
		raw, err := canonical.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("bootSelection")) || bytes.Contains(raw, []byte("serving")) {
			t.Fatal("new fields appeared in legacy bytes")
		}
		for _, value := range []string{"null", "{}"} {
			bad := bytes.Replace(raw, []byte(`"publication":{`), []byte(`"publication":{"bootSelection":`+value+`,`), 1)
			_, err := DecodeEnvironment(bad)
			requireCode(t, err, e.CodeInvalidCatalog)
		}
		if version == EnvironmentObjectStoresAPIVersion {
			for _, extra := range []string{`"mode":null,`, `"mode":"",`, `"serving":null,`, `"serving":{},`} {
				bad := bytes.Replace(raw, []byte(`"objectStores":[{`), []byte(`"objectStores":[{`+extra), 1)
				_, err := DecodeEnvironment(bad)
				requireCode(t, err, e.CodeInvalidCatalog)
			}
		}
	}
}

func TestBootFieldsPreserveNativeYAMLVersionedRoundTrip(t *testing.T) {
	maintained, err := LoadEnvironment(t.Context(), "../../../inputs/environments/github-release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{EnvironmentAPIVersion, EnvironmentObjectStoresAPIVersion, EnvironmentBootAPIVersion} {
		t.Run(version, func(t *testing.T) {
			environment := maintained.Clone()
			switch version {
			case EnvironmentObjectStoresAPIVersion:
				environment.Publication = objectEnvironment().Publication
			case EnvironmentBootAPIVersion:
				environment.Publication = bootEnvironmentFixture().Publication
			}
			environment.APIVersion = version
			environment.Publication.Transports = []string{PublicationTransportOCI}
			before, err := environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := nativeyaml.Marshal(environment)
			if err != nil {
				t.Fatal(err)
			}
			if version != EnvironmentBootAPIVersion && (bytes.Contains(bytes.ToLower(raw), []byte("bootselection:")) || bytes.Contains(raw, []byte("serving:"))) {
				t.Fatal("native YAML added boot fields to a legacy Environment")
			}
			decoded, err := DecodeEnvironment(raw)
			if err != nil {
				t.Fatal(err)
			}
			after, err := decoded.Digest()
			if err != nil || before != after {
				t.Fatalf("native YAML roundtrip changed identity: before=%s after=%s err=%v", before, after, err)
			}
		})
	}
}
