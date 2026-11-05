// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Thirty seconds of bounded clock tolerance plus one second of STS expiry precision.
const objectSessionAllowance = 31 * time.Second

// No credential cache is involved: the synchronous STS call must finish before
// the store can be used. The admitted temporary session covers cleanup as well
// as operation work. No SDK-suppressed refresh can outlive its caller.
func objectSession(ctx context.Context, options S3ObjectStoreOptions, client *http.Client) (aws.CredentialsProvider, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, objectCapability("S3 session requires a bounded operation")
	}
	required := deadline.Add(options.CleanupTimeout)
	var session aws.Credentials
	if options.Credentials != nil {
		session = *options.Credentials
	} else {
		token, err := options.TokenSource(ctx)
		if err != nil {
			return nil, classifyContext(ctx, e.New(e.CodeTrustPolicyMissing, "workload identity token acquisition failed"))
		}
		if err := deliveryContext(ctx); err != nil {
			return nil, err
		}
		// STS owns JWT authentication; its documented compact-token resource bound is
		// 4..20000 bytes. https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRoleWithWebIdentity.html
		if len(token) < 4 || len(token) > 20000 {
			return nil, e.New(e.CodeTrustPolicyMissing, "workload identity token exceeds STS bounds")
		}
		// Request bounded clock tolerance without weakening actual expiration coverage.
		duration := max(15*time.Minute, ((time.Until(required)+time.Second-1)/time.Second)*time.Second+objectSessionAllowance)
		if duration > 12*time.Hour {
			return nil, e.New(e.CodeTrustPolicyMissing, "S3 operation exceeds maximum workload session duration")
		}
		provider := stscreds.NewWebIdentityRoleProvider(sts.New(sts.Options{Region: options.Region, HTTPClient: client, Credentials: aws.AnonymousCredentials{},
			Retryer: retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = options.MaxAttempts })}), options.RoleARN, objectIdentityToken(bytes.Clone(token)),
			func(o *stscreds.WebIdentityRoleOptions) {
				o.RoleSessionName = options.SessionName
				o.Duration = duration
			})
		session, err = provider.Retrieve(ctx)
		if err != nil {
			return nil, classifyContext(ctx, e.New(e.CodeTrustPolicyMissing, "temporary S3 workload session failed"))
		}
	}
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if session.AccessKeyID == "" || session.SecretAccessKey == "" || session.SessionToken == "" || !session.CanExpire {
		return nil, e.New(e.CodeTrustPolicyMissing, "S3 requires an explicit temporary session identity")
	}
	if session.Expires.Before(required) {
		return nil, e.New(e.CodeUnavailable, "temporary S3 session lifetime does not cover operation and cleanup; check session duration and clock")
	}

	return credentials.StaticCredentialsProvider{Value: session}, nil
}

type objectIdentityToken []byte

func (token objectIdentityToken) GetIdentityToken() ([]byte, error) { return bytes.Clone(token), nil }
