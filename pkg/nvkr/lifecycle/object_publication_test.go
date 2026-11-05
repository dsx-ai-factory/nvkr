// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestObjectPublicationJoinsActualLocalDescriptor(t *testing.T) {
	const invocation = "https://github.com/example/project/actions/runs/1/attempts/1"
	hash := func(letter string) string { return "sha256:" + strings.Repeat(letter, 64) }
	for _, kind := range []string{"executable", "apt-repository", "debian-package", "disk-image", "oci", "helm-oci", "bundle-oci"} {
		t.Run(kind, func(t *testing.T) {
			for _, scenario := range []string{"exact", "different descriptor", "different raw subject", "different inputs", "different definition", "different environment", "different composition", "different invocation", "different output", "extra output", "different build"} {
				t.Run(scenario, func(t *testing.T) {
					metadata := delivery.ReleaseMetadata{DefinitionDigest: hash("1"), EnvironmentDigest: hash("2"), PlanDigest: hash("3"), InvocationID: invocation}
					origin := &delivery.PublicationOrigin{Ref: "example.test/product/release@" + hash("4"), PreparedInvocationID: invocation, SignedInvocationID: invocation}
					output := delivery.PublicationOutput{Output: delivery.Output{Name: "selected", Kind: kind, Digest: hash("5"), BuildDigest: hash("6"), Components: []string{"producer"}},
						Publication: origin, DescriptorDigest: hash("7"), ArtifactType: "application/octet-stream"}
					aggregate := delivery.PublicationDescription{Metadata: metadata, Outputs: []delivery.PublicationOutput{output}}
					localOutput := output
					localOutput.Publication = nil
					localOutput.Reference = "example.test/original/selected@" + hash("7")
					local := delivery.PublicationDescription{Reference: origin.Ref, Metadata: metadata, Outputs: []delivery.PublicationOutput{localOutput}}
					switch scenario {
					case "different descriptor":
						// This is the security regression: both metadata views have
						// equal logical inputs and raw/semantic subjects, but the
						// local wrapper contains different actual bytes.
						local.Outputs[0].DescriptorDigest = hash("8")
					case "different raw subject":
						local.Outputs[0].Digest = hash("8")
					case "different inputs":
						local.Metadata.PlanDigest = hash("8")
					case "different definition":
						local.Metadata.DefinitionDigest = hash("8")
					case "different environment":
						local.Metadata.EnvironmentDigest = hash("8")
					case "different composition":
						local.Metadata.Composition = &definition.CompositionRecord{Metadata: definition.Metadata{Name: "different-composition"}}
					case "different invocation":
						local.Metadata.InvocationID = "https://github.com/example/project/actions/runs/2/attempts/1"
					case "different output":
						local.Outputs[0].Name = "sibling"
					case "extra output":
						local.Outputs = append(local.Outputs, localOutput)
					case "different build":
						local.Outputs[0].BuildDigest = hash("8")
					}
					err := requireMirrorOutput(aggregate, output, local)
					if scenario == "exact" {
						if err != nil {
							t.Fatal(err)
						}
					} else {
						assertCode(t, err, e.CodeVerifyFailed)
					}
				})
			}
		})
	}
}

type objectTokenTransport func(*http.Request) (*http.Response, error)

func (transport objectTokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestObjectPublicationGitHubTokenBoundsAndScope(t *testing.T) {
	for _, scenario := range []string{"exact", "wrong origin", "missing authority", "denied", "redirect", "oversized", "empty token", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			values := map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": "https://token.actions.githubusercontent.com/request?existing=value", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "synthetic-request-token"}
			if scenario == "wrong origin" {
				values["ACTIONS_ID_TOKEN_REQUEST_URL"] = "https://untrusted.example.test/request"
			}
			if scenario == "missing authority" {
				delete(values, "ACTIONS_ID_TOKEN_REQUEST_TOKEN")
			}
			calls := 0
			client := &http.Client{Transport: objectTokenTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Context().Err() != nil {
					return nil, request.Context().Err()
				}
				if request.URL.Query().Get("audience") != "sts.amazonaws.com" || request.URL.Query().Get("existing") != "value" || request.Header.Get("Authorization") != "Bearer synthetic-request-token" {
					t.Fatal("request does not bind AWS audience and scoped GitHub request authority")
				}
				status, body := http.StatusOK, `{"value":"synthetic-oidc-token"}`
				if scenario == "denied" {
					status = http.StatusForbidden
				}
				if scenario == "oversized" {
					body = strings.Repeat("x", (32<<10)+1)
				}
				if scenario == "empty token" {
					body = `{"value":""}`
				}
				responseRequest := request
				if scenario == "redirect" {
					responseRequest = request.Clone(request.Context())
					parsed := *request.URL
					parsed.Path = "/other"
					responseRequest.URL = &parsed
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: responseRequest}, nil
			})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			token, err := objectGitHubToken(ctx, client, func(name string) string { return values[name] })
			if scenario == "exact" {
				if err != nil || string(token) != "synthetic-oidc-token" {
					t.Fatalf("token response: %q %v", token, err)
				}
			} else {
				if err == nil || len(token) != 0 {
					t.Fatal("invalid identity response was accepted")
				}
				if strings.Contains(err.Error(), "synthetic-request-token") || strings.Contains(err.Error(), "synthetic-oidc-token") {
					t.Fatal("credential leaked in error")
				}
			}
			if (scenario == "wrong origin" || scenario == "missing authority") && calls != 0 {
				t.Fatal("invalid admission reached network")
			}
		})
	}
}

func TestObjectPublicationRefusesAdapterWithoutPortableProof(t *testing.T) {
	fixture := newExactInputFixture(t)
	consumer := fixture.consumer(t, nil)
	consumer.materialize = func(context.Context, string) (*delivery.Publication, error) {
		t.Fatal("unsupported verifier reached registry")
		return nil, nil
	}
	_, err := consumer.portableVerifier(exactInputPolicy())
	assertCode(t, err, e.CodeEngineCapability)
	_, _, err = consumer.VerifyObjectPublication(t.Context(), nil, delivery.StoredObject{}, ObjectPublicationVerification{})
	assertCode(t, err, e.CodeUsage)
}
