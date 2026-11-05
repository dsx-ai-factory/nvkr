// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

// ExecuteGitHubObjectPublication is the ordinary required publication operation.
// Factory owns hosted admission and reporting; lifecycle joins the original
// exact-input proof and payload owners before the byte transport can write.
func ExecuteGitHubObjectPublication(ctx context.Context, plan *factory.Plan, nodeID, generatorVersion string, options factory.GitHubStepOptions) error {
	if ctx == nil || plan == nil || plan.GeneratorVersion != generatorVersion {
		return e.New(e.CodePipelineDrift, "object publication requires its exact generated Plan")
	}
	return factory.RunGitHubObjectPublicationNode(ctx, plan, nodeID, options, func(ctx context.Context, plan *factory.Plan, node factory.Node, options factory.GitHubStepOptions) (resultErr error) {
		if runtime.GOOS != "linux" || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" {
			return e.New(e.CodeEngineCapability, "generated object publication requires the admitted Linux GitHub execution")
		}
		destination, _, err := factory.ObjectPublicationForNode(plan, node.ID)
		if err != nil {
			return err
		}
		limits := destination.EffectiveTransfer()
		ctx, cancel := context.WithTimeout(ctx, time.Duration(limits.OperationTimeoutSeconds)*time.Second)
		defer cancel()
		now := time.Now().UTC()
		invocation, err := provenance.NewGitHubRunContext(os.Getenv, now, now)
		if err != nil {
			return err
		}
		identity, err := regexp.Compile(plan.Environment.Signing.Identity)
		if err != nil || !identity.MatchString(invocation.BuilderID()) {
			return e.New(e.CodeUntrustedSigner, "object publication workflow differs from the planned signing identity")
		}
		signing, registry, err := factory.ObjectPublicationSourceOptions(ctx, filepath.Join(".nvkr", "factory"), plan, node.ID)
		if err != nil {
			return err
		}
		reference, err := factory.ObjectPublicationSourceReference(ctx, filepath.Join(".nvkr", "factory"), plan, node.ID)
		if err != nil {
			return err
		}
		expectation, err := PrepareExactInputs(plan.Definition, plan.Environment, plan)
		if err != nil {
			return err
		}
		consumer, err := NewConsumer(ConsumerOptions{CosignBinary: signing.Binary, CosignDigest: signing.BinaryDigest,
			Credential: signing.Credential, Registry: registry})
		if err != nil {
			return err
		}
		role := os.Getenv("NVKR_S3_ROLE_ARN")
		if role == "" {
			return e.New(e.CodeTrustPolicyMissing, "object publication workload role reference is empty")
		}
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return e.New(e.CodeEngineCapability, "object publication requires an owned HTTP transport")
		}
		transport := base.Clone()
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		s3 := destination.S3
		store, err := delivery.NewObjectStore(ctx, delivery.ObjectStoreOptions{Provider: destination.Provider, S3: delivery.S3ObjectStoreOptions{
			Region: s3.Region, Endpoint: s3.Endpoint, Bucket: s3.Bucket, Prefix: s3.Prefix, Addressing: s3.Addressing,
			ExpectedBucketOwner: s3.ExpectedBucketOwner, RoleARN: role, SessionName: "nvkr-" + os.Getenv("GITHUB_RUN_ID") + "-" + os.Getenv("GITHUB_RUN_ATTEMPT"),
			OperationTimeout: time.Duration(limits.OperationTimeoutSeconds) * time.Second,
			TokenSource:      func(ctx context.Context) ([]byte, error) { return objectGitHubToken(ctx, client, os.Getenv) },
		}})
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
		policy := VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication,
			IdentityRegexp: signing.Identity, Issuer: signing.Issuer, MinimumDiskEvidence: DiskEvidenceBuildProvenance}
		locator, err := consumer.MirrorExactInputs(ctx, reference, policy, expectation, invocation.InvocationID(), store,
			delivery.MirrorLimits{MaxObjectBytes: int64(limits.MaxObjectBytes), MaxTotalBytes: int64(limits.MaxTotalBytes), MaxObjects: int(limits.MaxObjects)})
		if err != nil {
			return err
		}
		// The normal node's output is a transport observation. Run success still
		// requires this job and every other original graph job to have succeeded.
		raw, err := canonical.Marshal(locator)
		if err != nil {
			return err
		}
		for _, artifact := range plan.Graph.Artifacts {
			if len(node.Outputs) != 1 || artifact.ID != node.Outputs[0] {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(artifact.Path), 0o700); err != nil {
				return e.Wrap(e.CodeImmutableViolation, err, "creating object publication record directory")
			}
			file, err := os.OpenFile(artifact.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return e.Wrap(e.CodeImmutableViolation, err, "creating object publication observation")
			}
			_, writeErr := file.Write(raw)
			if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
				return e.Wrap(e.CodeImmutableViolation, err, "recording object publication observation")
			}
			if options.Stdout != nil {
				fmt.Fprintf(options.Stdout, "Published original signed artifact closure: s3://%s/%s/%s\n", s3.Bucket, s3.Prefix, locator.Key)
			}
			return nil
		}
		return e.New(e.CodePipelineDrift, "object publication has no exact output record")
	})
}

func objectGitHubToken(ctx context.Context, client *http.Client, lookup func(string) string) (tokenBytes []byte, resultErr error) {
	if ctx == nil || client == nil || lookup == nil {
		return nil, e.New(e.CodeUsage, "GitHub identity request requires context, client and environment")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "GitHub identity request canceled")
	}
	endpoint, err := url.Parse(lookup("ACTIONS_ID_TOKEN_REQUEST_URL"))
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Fragment != "" || !strings.HasSuffix(endpoint.Hostname(), ".actions.githubusercontent.com") {
		return nil, e.New(e.CodeTrustPolicyMissing, "object publication requires the trusted GitHub OIDC endpoint")
	}
	access := lookup("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if access == "" {
		return nil, e.New(e.CodeTrustPolicyMissing, "object publication job lacks OIDC request authority")
	}
	query := endpoint.Query()
	query.Set("audience", "sts.amazonaws.com")
	endpoint.RawQuery = query.Encode()
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(bounded, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "invalid GitHub OIDC request")
	}
	request.Header.Set("Authorization", "Bearer "+access)
	response, err := client.Do(request)
	if err != nil {
		return nil, e.New(e.CodeUnavailable, "GitHub OIDC request failed")
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			resultErr = errors.Join(resultErr, e.New(e.CodeUnavailable, "closing GitHub identity response failed"))
		}
		if err := bounded.Err(); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeTimeout, err, "GitHub identity request canceled"))
		}
		if resultErr != nil {
			tokenBytes = nil
		}
	}()
	if response.StatusCode != http.StatusOK || response.Request == nil || response.Request.URL.String() != endpoint.String() {
		return nil, e.New(e.CodeTrustPolicyMissing, "GitHub OIDC request was denied or redirected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (32<<10)+1))
	if err != nil || len(raw) > 32<<10 {
		return nil, e.New(e.CodeTrustPolicyMissing, "GitHub OIDC response is incomplete or oversized")
	}
	var token struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &token); err != nil || len(token.Value) < 4 || len(token.Value) > 20000 {
		return nil, e.New(e.CodeTrustPolicyMissing, "GitHub OIDC response has no bounded identity token")
	}
	return []byte(token.Value), nil
}
