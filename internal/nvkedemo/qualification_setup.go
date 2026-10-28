// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"go.yaml.in/yaml/v3"
)

// SetupQualification provisions disposable local trust for an explicitly
// selected prepared demo. It creates descriptions, never verification receipts.
// Existing keys, policy files, registries and evidence are never reused.
func SetupQualification(ctx context.Context, file DemoFile, out string, progress io.Writer) (result string, err error) {
	retained := ""
	defer func() { err = qualificationSetupFailure(err, retained) }()
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return "", e.New(e.CodeEngineCapability, "qualification setup requires native Linux ARM64")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if file.Build.QualifyLocal || file.Build.Qualification != (QualificationFile{}) || file.Build.QualificationPolicy != "" || file.Build.Prepared {
		return "", configUsage("setup requires a base demo configuration without existing qualification or prepared settings")
	}
	cfg, err := file.BuildConfig(progress)
	if err != nil {
		return "", err
	}
	cfg.Network, err = normalizeNetwork(file.Run.Network)
	if err != nil {
		return "", err
	}
	cfg.Repository, err = resolveExisting(cfg.Repository)
	if err != nil {
		return "", err
	}
	if cfg.AttemptPrefix == "" || strings.ContainsAny(cfg.AttemptPrefix, "/\\\x00\r\n\t ") || strings.HasPrefix(cfg.AttemptPrefix, "-") {
		return "", configUsage("require a simple prepared attempt name")
	}
	if _, err := preparedBuildInvocation(cfg); err != nil {
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(cfg.Repository, ".nvkr/runs", cfg.AttemptPrefix)); !errors.Is(err, os.ErrNotExist) {
		return "", configUsage("setup requires a prepared attempt with no existing producer Run")
	}
	plan, err := factory.LoadPlanContext(ctx, filepath.Join(cfg.Repository, ".nvkr/laptop-bootstrap", cfg.AttemptPrefix, "selected-plan.json"))
	if err != nil {
		return "", err
	}
	// Admission with throwaway public keys precedes download, key files and Docker
	// mutations. The final policy is rebuilt with the actual generated role keys.
	keys, err := setupAdmissionKeys()
	if err != nil {
		return "", err
	}
	if _, err := demoQualificationPolicy(plan, cfg.Network, keys); err != nil {
		return "", err
	}
	if out == "" || strings.ContainsAny(out, "\x00\r\n,") {
		return "", configUsage("setup requires a fresh output directory without commas or control characters")
	}
	parent, err := resolveExisting(filepath.Dir(out))
	if err != nil {
		return "", err
	}
	out = filepath.Join(parent, filepath.Base(out))
	held, _, err := securepath.OpenDirectory(parent)
	if err != nil {
		return "", err
	}
	available, capacityErr := securepath.AvailableDiskBackedBytes(ctx, held)
	_ = held.Close()
	if capacityErr != nil || available < 1<<30 {
		return "", errors.Join(capacityErr, configUsage("qualification setup requires at least 1 GiB disk-backed headroom; final payload capacity is checked later"))
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return "", err
	}
	retained = out
	for _, name := range []string{"scratch", "registry-data", "diagnostics"} {
		if err := os.Mkdir(filepath.Join(out, name), 0700); err != nil {
			return "", err
		}
	}
	runner := Runner{Work: filepath.Join(out, "diagnostics"), Env: NativeEnvironment(), Progress: progress}
	cosign := filepath.Join(out, "cosign")
	if err := runner.Run(ctx, []string{"curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--connect-timeout", "30", "--max-time", "300", "--max-filesize", "134217728", "--output", cosign, demoCosignURL}, "download-cosign", 6*time.Minute); err != nil {
		return "", err
	}
	if err := checkSetupCosign(ctx, cosign); err != nil {
		return "", err
	}
	if err := os.Chmod(cosign, 0700); err != nil {
		return "", err
	}
	keyRunner := runner
	keyRunner.Env = SetEnv(runner.Env, map[string]string{"COSIGN_PASSWORD": ""})
	for i, role := range []string{"builder", "smoke", "qualifier"} {
		prefix := filepath.Join(out, role)
		if err := keyRunner.Run(ctx, []string{cosign, "generate-key-pair", "--output-key-prefix", prefix}, "generate-"+role, time.Minute); err != nil {
			return "", err
		}
		if err := os.Chmod(prefix+".key", 0600); err != nil {
			return "", err
		}
		raw, err := boundedRegularRead(prefix+".pub", 64<<10)
		if err != nil {
			return "", err
		}
		keys[i] = string(raw)
	}
	policy, err := demoQualificationPolicy(plan, cfg.Network, keys)
	if err != nil {
		return "", err
	}
	raw, err := canonical.Marshal(policy)
	if err != nil {
		return "", err
	}
	policyPath := filepath.Join(out, "policy.json")
	if err := privateWrite(policyPath, raw); err != nil {
		return "", err
	}
	q := QualificationFile{BuilderKey: filepath.Join(out, "builder.key"), SmokeKey: filepath.Join(out, "smoke.key"), QualifierKey: filepath.Join(out, "qualifier.key"), Cosign: cosign,
		Scratch: filepath.Join(out, "scratch"), EvidenceOut: filepath.Join(out, "evidence.json"), Repository: "localhost:5000/local-m3", PlainHTTP: true, Anonymous: true}
	cfg.QualificationPolicy, cfg.Qualification = policyPath, q
	// Recompile with the existing bootstrap and authenticate all private key roles
	// before starting the registry. No stored preview authorizes a build.
	env, err := localBuildEnvironment(ctx)
	if err != nil {
		return "", err
	}
	if cfg.NVKEClone != "" {
		clone, err := resolveExisting(cfg.NVKEClone)
		if err != nil {
			return "", err
		}
		file.Build.NVKEClone = clone
		env = SetEnv(env, map[string]string{"NVKE_LOCAL_CLONE": clone})
	}
	if err := recheckPreparedQualificationPlan(ctx, Runner{Env: env, Progress: progress}, cfg, policy); err != nil {
		return "", err
	}
	if err := preflightDemoQualification(ctx, cfg, plan); err != nil {
		return "", err
	}
	if err := runner.Run(ctx, []string{"docker", "pull", demoSmokeImage}, "pull-smoke-image", 5*time.Minute); err != nil {
		return "", err
	}
	registry, err := startQualificationRegistry(ctx, runner, out)
	if err != nil {
		return "", err
	}
	// If config retention fails, stop only this exact newly created container.
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			err = errors.Join(err, stopQualificationRegistry(cleanup, Runner{Env: NativeEnvironment()}, out, registry))
		}
	}()
	q.Repository = "localhost:" + registry.Port + "/local-m3"
	file.Build.Repo, file.Build.Prepared = cfg.Repository, true
	file.Build.QualificationPolicy, file.Build.Qualification = policyPath, q
	file.Run.Network = cfg.Network
	raw, err = yaml.Marshal(file)
	if err != nil {
		return "", err
	}
	result = filepath.Join(out, "demo.yaml")
	return result, privateWrite(result, raw)
}

func setupAdmissionKeys() (keys [3]string, err error) {
	for i := range keys {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return keys, err
		}
		raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return keys, err
		}
		keys[i] = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}))
	}
	return keys, nil
}

func checkSetupCosign(ctx context.Context, path string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	held, info, err := securepath.OpenRegular(path)
	if err != nil {
		return err
	}
	defer held.Close()
	if info.Size() <= 0 || info.Size() > 128<<20 {
		return e.New(e.CodeUnpinnedInput, "demo cosign executable size is outside its bound")
	}
	hash := sha256.New() // Exact executable blob integrity, not structured identity.
	if _, err := transportarchive.CopyExact(ctx, hash, held, info.Size()); err != nil {
		return err
	}
	after, err := held.Stat()
	if err != nil {
		return err
	}
	if !securepath.SameMetadata(info, after) || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != demoCosignDigest {
		return e.New(e.CodeUnpinnedInput, "downloaded demo cosign differs from its maintained executable pin")
	}
	return nil
}

// StopQualificationRegistry stops the exact owned registry, retaining its
// container, disk-backed data, keys, public policy and evidence for inspection.
func StopQualificationRegistry(ctx context.Context, out string) error {
	if err := requireContext(ctx); err != nil {
		return e.Wrap(e.CodeUsage, err, "qualification registry context")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return e.New(e.CodeEngineCapability, "qualification registry stop requires native Linux ARM64")
	}
	root, err := resolveExisting(out)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "qualification setup directory")
	}
	raw, err := boundedRegularRead(filepath.Join(root, "registry.json"), 16<<10)
	if err != nil {
		return e.Wrap(e.CodePreflightFailed, err, "read retained qualification registry description")
	}
	var registry qualificationRegistry
	if err := canonical.UnmarshalExact(raw, &registry); err != nil {
		return e.Wrap(e.CodePreflightFailed, err, "decode qualification registry description")
	}
	if err := stopQualificationRegistry(ctx, Runner{Env: NativeEnvironment()}, root, registry); err != nil {
		return e.Wrap(e.CodePreflightFailed, err, "stop owned qualification registry")
	}
	return nil
}

func validateAutomaticQualification(enabled, prepared bool, policy string, q QualificationFile) error {
	if enabled && (prepared || policy != "" || q != (QualificationFile{})) {
		return configUsage("qualifyLocal requires a fresh base configuration without prepared, qualificationPolicy or qualification settings")
	}
	return nil
}

// Registry lifetime ends only after qualification's independent read-back. A
// canceled build must still stop its owned service; stop failure blocks handoff.
func finishAutomaticQualification(ctx context.Context, out string, stop func(context.Context, string) error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	return stop(cleanup, out)
}

// Failed private setup is preserved; only a fresh destination may be retried.
func qualificationSetupFailure(err error, retained string) error {
	if err == nil {
		return nil
	}
	code := e.CodePreflightFailed
	var coded *e.Error
	if errors.As(err, &coded) {
		code = coded.Code
	}
	if retained != "" {
		return e.Wrap(code, err, "local qualification setup failed; files retained at %q are not successful evidence; manual setup requires a fresh --out; automatic --qualify-local requires a fresh --attempt", retained)
	}
	return e.Wrap(code, err, "local qualification setup failed before creating setup files")
}
