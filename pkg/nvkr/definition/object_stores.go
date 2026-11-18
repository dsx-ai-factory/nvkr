// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"io/fs"
	"math"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ObjectStoreDestination requires the complete selected publication at one
// immutable destination. Credentials remain references to workload roles.
type ObjectStoreDestination struct {
	Name     string                    `json:"name"`
	Mode     string                    `json:"mode,omitempty"`
	Provider string                    `json:"provider"`
	S3       *S3ObjectStoreDestination `json:"s3,omitempty"`
	Serving  *BootServing              `json:"serving,omitempty"`
	Transfer ObjectStoreTransfer       `json:"transfer,omitempty"`
}

type S3ObjectStoreDestination struct {
	Region              string `json:"region"`
	Endpoint            string `json:"endpoint,omitempty"`
	Bucket              string `json:"bucket"`
	Prefix              string `json:"prefix"`
	Addressing          string `json:"addressing,omitempty"`
	ExpectedBucketOwner string `json:"expectedBucketOwner"`
	RoleRef             string `json:"roleRef"`
}

type ObjectStoreTransfer struct {
	MaxObjectBytes          uint64 `json:"maxObjectBytes,omitempty"`
	MaxTotalBytes           uint64 `json:"maxTotalBytes,omitempty"`
	MaxObjects              uint64 `json:"maxObjects,omitempty"`
	OperationTimeoutSeconds uint64 `json:"operationTimeoutSeconds,omitempty"`
}

// EffectiveTransfer applies bounded defaults without rewriting authored bytes.
func (destination ObjectStoreDestination) EffectiveTransfer() ObjectStoreTransfer {
	limits := destination.Transfer
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = 256 << 30
	}
	if limits.MaxObjectBytes == 0 {
		limits.MaxObjectBytes = min(64<<30, limits.MaxTotalBytes)
	}
	if limits.MaxObjects == 0 {
		limits.MaxObjects = 8192
	}
	if limits.OperationTimeoutSeconds == 0 {
		limits.OperationTimeoutSeconds = 1200
	}
	return limits
}

func (environment Environment) validateObjectStores() error {
	if err := environment.validateBootPublication(); err != nil {
		return err
	}
	var destinations []ObjectStoreDestination
	if environment.Publication != nil {
		destinations = environment.Publication.ObjectStores
	}
	if environment.APIVersion == EnvironmentAPIVersion {
		if destinations != nil {
			return e.New(e.CodeInvalidCatalog, "objectStores requires Environment v2")
		}
		return nil
	}
	if len(destinations) == 0 || len(destinations) > 16 {
		return e.New(e.CodeInvalidCatalog, "object-store Environment requires one to sixteen destinations")
	}
	if environment.GitHubDevelopment {
		return e.New(e.CodeEngineCapability, "development execution does not admit object-store publication")
	}
	if environment.Signing == nil || environment.Signing.Mode != "cosign" {
		return e.New(e.CodeEngineCapability, "object publication requires original signed publication")
	}
	for key := range environment.Runtime.Variables {
		if strings.HasPrefix(key, "NVKR_S3_") {
			return e.New(e.CodeTrustPolicyMissing, "object workload credentials cannot be authored as runtime variables")
		}
	}
	previous := ""
	for _, destination := range destinations {
		if !namePattern.MatchString(destination.Name) || destination.Name <= previous || len(destination.Name) > 64 {
			return e.New(e.CodeInvalidCatalog, "object-store destinations must have sorted unique canonical names")
		}
		previous = destination.Name
		if err := destination.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks one destination description without acquiring credentials or
// creating a store. Environment admission additionally owns schema vocabulary,
// signer/route admission, complete selection and sorted required destinations.
func (destination ObjectStoreDestination) Validate() error {
	if !namePattern.MatchString(destination.Name) || len(destination.Name) > 64 {
		return e.New(e.CodeInvalidCatalog, "object-store destination requires a canonical name")
	}
	switch destination.Mode {
	case "", ObjectStoreFullMirrorMode:
		if destination.Serving != nil {
			return e.New(e.CodeInvalidCatalog, "full-mirror destination cannot declare boot serving")
		}
	case ObjectStoreBootMode:
		if destination.Serving == nil {
			return e.New(e.CodeInvalidCatalog, "selective boot destination requires serving configuration")
		}
		if err := destination.Serving.Validate(); err != nil {
			return err
		}
	default:
		return e.New(e.CodeEngineCapability, "unsupported object-store publication mode")
	}
	if destination.Provider != "s3" || destination.S3 == nil {
		return e.New(e.CodeEngineCapability, "object-store publication supports the s3 provider")
	}
	s3 := destination.S3
	if s3.Region == "" || len(s3.Region) > 64 || strings.ContainsFunc(s3.Region, func(c rune) bool { return c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') }) {
		return e.New(e.CodeInvalidCatalog, "S3 publication requires an explicit region label")
	}
	if s3.Bucket == "" || len(s3.Bucket) > 255 || !utf8.ValidString(s3.Bucket) || strings.ContainsFunc(s3.Bucket, unicode.IsControl) || strings.ContainsAny(s3.Bucket, "/\\?#@ \t\r\n") {
		return e.New(e.CodeInvalidCatalog, "S3 publication requires an explicit bucket name")
	}
	if s3.Prefix == "." || !fs.ValidPath(s3.Prefix) || len(s3.Prefix) > 512 || !utf8.ValidString(s3.Prefix) || strings.ContainsAny(s3.Prefix, "\\") || strings.ContainsFunc(s3.Prefix, unicode.IsControl) {
		return e.New(e.CodeInvalidCatalog, "S3 publication prefix must be a canonical relative literal key")
	}
	if s3.Endpoint != "" {
		endpoint, err := url.Parse(s3.Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || strings.ContainsAny(s3.Endpoint, "?#") || endpoint.Path != "" || !utf8.ValidString(s3.Endpoint) || strings.ContainsFunc(s3.Endpoint, unicode.IsControl) {
			return e.New(e.CodeInvalidCatalog, "S3 endpoint must be an HTTPS origin")
		}
	}
	if s3.Addressing != "" && s3.Addressing != "virtual" && s3.Addressing != "path" {
		return e.New(e.CodeInvalidCatalog, "S3 addressing must be virtual or path")
	}
	if len(s3.ExpectedBucketOwner) != 12 || strings.ContainsFunc(s3.ExpectedBucketOwner, func(c rune) bool { return c < '0' || c > '9' }) {
		return e.New(e.CodeTrustPolicyMissing, "S3 publication requires an exact twelve-digit expected bucket owner")
	}
	if !githubCredentialRefPattern.MatchString(s3.RoleRef) || strings.HasPrefix(strings.ToUpper(strings.TrimPrefix(s3.RoleRef, "github:")), "GITHUB_") {
		return e.New(e.CodeTrustPolicyMissing, "S3 workload role must reference github:VARIABLE_NAME")
	}
	limits := destination.EffectiveTransfer()
	// Reserve cleanup plus the provider's 30-second skew and one-second precision margin.
	if limits.MaxObjectBytes > 64<<30 || limits.MaxTotalBytes > math.MaxInt64 || limits.MaxTotalBytes < limits.MaxObjectBytes || limits.MaxObjects > 32768 || limits.OperationTimeoutSeconds > 3509 {
		return e.New(e.CodeEngineCapability, "object-store transfer exceeds admitted inventory or operation bounds")
	}
	return nil
}
