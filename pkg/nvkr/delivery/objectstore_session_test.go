// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type objectSessionHTTP func(*http.Request) (*http.Response, error)

func (transport objectSessionHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestObjectStoreWorkloadSessionIsSynchronousAndFixed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(100 * time.Millisecond)
		testObjectStoreWorkloadSession(t, 0, "")
	})
}

func testObjectStoreWorkloadSession(t *testing.T, skew time.Duration, want e.Code) {
	options := objectTestOptions()
	options.Credentials = nil
	options.RoleARN = "arn:aws:iam::123456789012:role/synthetic"
	options.SessionName = "synthetic"
	options.OperationTimeout, options.CleanupTimeout = 0, 0
	calls, tokens := 0, 0
	options.TokenSource = func(ctx context.Context) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		tokens++
		return []byte("synthetic-workload-token"), nil
	}
	options, err := admitObjectOptions(options)
	if err != nil || options.OperationTimeout != time.Hour-time.Minute-objectSessionAllowance || options.CleanupTimeout != time.Minute {
		t.Fatalf("default session bounds: %v", err)
	}
	client := &http.Client{Transport: objectSessionHTTP(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Context().Err() != nil {
			t.Fatal("STS request lacked live caller context")
		}
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		duration, _ := strconv.Atoi(values.Get("DurationSeconds"))
		if duration < 900 || duration > 3600 || values.Get("RoleArn") != options.RoleARN || values.Get("WebIdentityToken") != "synthetic-workload-token" {
			t.Fatal("SDK did not bind token, role and session duration")
		}
		response := fmt.Sprintf(`<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>synthetic-access</AccessKeyId><SecretAccessKey>synthetic-secret</SecretAccessKey><SessionToken>synthetic-session</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, time.Now().Add(time.Duration(duration)*time.Second-skew).UTC().Format(time.RFC3339))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
	})}
	ctx, cancel := context.WithTimeout(t.Context(), options.OperationTimeout)
	defer cancel()
	provider, err := objectSession(ctx, options, client)
	if want != "" {
		requireObjectCode(t, err, want)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		credentials, err := provider.Retrieve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !credentials.CanExpire || credentials.SessionToken == "" {
			t.Fatal("lost temporary-session identity")
		}
	}
	if calls != 1 || tokens != 1 {
		t.Fatalf("unexpected background/refresh calls: STS=%d token=%d", calls, tokens)
	}
}

func TestObjectStoreWorkloadSessionRefusals(t *testing.T) {
	for _, test := range []struct {
		name   string
		source func(context.Context) ([]byte, error)
	}{
		{"source failure", func(context.Context) ([]byte, error) { return nil, errors.New("synthetic-secret") }},
		{"too short", func(context.Context) ([]byte, error) { return []byte("abc"), nil }},
		{"too long", func(context.Context) ([]byte, error) { return make([]byte, 20001), nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := objectTestOptions()
			options.Credentials = nil
			options.RoleARN = "arn:aws:iam::123456789012:role/synthetic"
			options.TokenSource = test.source
			client := &http.Client{Transport: objectSessionHTTP(func(*http.Request) (*http.Response, error) { t.Fatal("unadmitted STS I/O"); return nil, nil })}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := objectSession(ctx, options, client)
			requireObjectCode(t, err, e.CodeTrustPolicyMissing)
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("credential source error leaked")
			}
		})
	}
}

func TestObjectStoreCanceledSTSUsesCallerContext(t *testing.T) {
	options := objectTestOptions()
	options.Credentials = nil
	options.RoleARN = "arn:aws:iam::123456789012:role/synthetic"
	options.TokenSource = func(context.Context) ([]byte, error) { return []byte("synthetic-token"), nil }
	entered := make(chan struct{})
	stopped := make(chan struct{})
	client := &http.Client{Transport: objectSessionHTTP(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(stopped)
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, err := objectSession(ctx, options, client); returned <- err }()
	<-entered
	cancel()
	requireObjectCode(t, <-returned, e.CodeTimeout)
	select {
	case <-stopped:
	default:
		t.Fatal("session returned before actual STS request stopped")
	}
}

func TestObjectStoreRefusesSessionAboveTwelveHoursBeforeSTS(t *testing.T) {
	options := objectTestOptions()
	options.Credentials = nil
	options.RoleARN = "arn:aws:iam::123456789012:role/synthetic"
	options.TokenSource = func(context.Context) ([]byte, error) { return []byte("synthetic-token"), nil }
	client := &http.Client{Transport: objectSessionHTTP(func(*http.Request) (*http.Response, error) {
		t.Fatal("unadmitted session performed STS I/O")
		return nil, nil
	})}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Hour)
	defer cancel()
	_, err := objectSession(ctx, options, client)
	requireObjectCode(t, err, e.CodeTrustPolicyMissing)
}

func TestObjectStoreWorkloadSessionAllowsBoundedClockSkew(t *testing.T) {
	for _, phase := range []time.Duration{100 * time.Millisecond, 900 * time.Millisecond} {
		for _, skew := range []time.Duration{250 * time.Millisecond, 600 * time.Millisecond, 30 * time.Second, 31 * time.Second} {
			t.Run(fmt.Sprintf("phase_%s_skew_%s", phase, skew), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					time.Sleep(phase)
					var want e.Code
					if skew > 30*time.Second {
						want = e.CodeUnavailable
					}
					testObjectStoreWorkloadSession(t, skew, want)
				})
			})
		}
	}
}
