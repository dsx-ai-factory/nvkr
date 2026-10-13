// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

type Result struct {
	Observation      testobservation.Observation
	PrivateWorkspace string
	rawJUnit         []byte
}

// RawJUnit returns the original upstream file, including failures, without
// changing it into a public projection or an authenticated suite receipt.
func (r *Result) RawJUnit() []byte {
	if r == nil {
		return nil
	}
	return bytes.Clone(r.rawJUnit)
}

// Run admits all inputs before tool/file/cluster work, observes an existing
// cluster, invokes Hydrophone, preserves original results and attempts bounded
// cleanup even after failure/cancellation. PrivateWorkspace belongs to the caller;
// it contains private upstream diagnostics and must not be published wholesale.
func Run(ctx context.Context, o Options) (*Result, error) {
	return runWithClock(ctx, o, time.Now)
}

func runWithClock(ctx context.Context, o Options, now func() time.Time) (*Result, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "conformance requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "conformance canceled before admission")
	}
	if o.Observation.Environment.Role != "local" {
		return nil, e.New(e.CodeEngineCapability, "conformance execution is admitted only for local diagnostic clusters; upstream name-based automatic cleanup prevents site and tenant admission")
	}

	if !o.DisposableCluster {
		return nil, e.New(e.CodeUsage, "conformance execution requires explicit disposable-cluster acknowledgement; local role is only a declaration")
	}
	a, err := admit(ctx, o)
	if err != nil {
		return nil, err
	}
	defer a.close()
	workspace, err := os.MkdirTemp("", "nvkr-conformance-")
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "creating private conformance workspace")
	}
	r := &Result{Observation: o.Observation, PrivateWorkspace: workspace}
	started := strconv.FormatInt(now().UTC().Unix(), 10)
	startFact := testobservation.Fact{Name: "testStartedUnix", Value: started, Evidence: "declared", Phase: "before"}
	r.Observation.Environment.Facts = []testobservation.Fact{startFact}
	defer func() {
		r.Observation.Environment.Facts = append(r.Observation.Environment.Facts, testobservation.Fact{Name: "testFinishedUnix", Value: strconv.FormatInt(now().UTC().Unix(), 10), Evidence: "declared", Phase: "after"})
		sort.Slice(r.Observation.Environment.Facts, func(i, j int) bool {
			a, b := r.Observation.Environment.Facts[i], r.Observation.Environment.Facts[j]
			if a.Phase != b.Phase {
				return a.Phase < b.Phase
			}
			return a.Name < b.Name
		})
	}()

	r.Observation.Execution = testobservation.Outcome{Status: "not-run"}
	r.Observation.Cleanup = testobservation.Outcome{Status: "completed"}
	before, err := a.observe(ctx, workspace, "before")
	r.Observation.Environment = before
	r.Observation.Environment.Facts = append(r.Observation.Environment.Facts, startFact, testobservation.Fact{Name: "disposableCluster.acknowledged", Value: "true", Evidence: "declared", Phase: "before"})
	if err != nil {
		return r, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return r, e.Wrap(e.CodeInternal, err, "generating unique conformance namespace")
	}
	namespace := "nvkr-conformance-" + hex.EncodeToString(random[:])
	// Namespace names are never supplied by a caller. Reject collisions for all
	// upstream attempt resources before taking ownership of a fresh namespace.
	refs := resourceRefs(namespace)
	for _, ref := range refs {
		found, err := a.getObject(ctx, workspace, ref)
		if err != nil {
			return r, err
		}
		if found.Metadata.UID != "" {
			return r, failure("unique conformance attempt resource already exists")
		}
	}
	raw, _, err := a.kube(ctx, workspace, nil, "create", "namespace", namespace, "-o", "json")
	if err != nil {
		r.Observation.Execution.Status = "infrastructure-error"
		r.Observation.Cleanup.Status = "unknown"
		return r, err
	}
	var ns object
	if err = decodeAPI(raw, &ns); err != nil || ns.Metadata.UID == "" {
		r.Observation.Execution.Status = "infrastructure-error"
		r.Observation.Cleanup.Status = "unknown"
		return r, failure("created namespace UID unavailable; cleanup ownership not established")
	}
	owned := map[string]string{refs[0].path: ns.Metadata.UID}
	runArgs := []string{"--conformance", "--disable-progress-status", "--namespace=" + namespace, "--skip-preflight=" + namespace, "--conformance-image=" + o.Image, "--busybox-image=" + o.BusyboxImage, "--output-dir=" + workspace, "--parallel=1", "--verbosity=1", "--startup-timeout=" + a.options.StartupTimeout.String()}
	_, code, runErr := a.command(ctx, a.hydrophone, runArgs, workspace, nil, o.RunTimeout)
	r.Observation.Execution = testobservation.Outcome{Status: "completed", ExitCode: code}
	if runErr != nil {
		r.Observation.Execution.Status = "failed"
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			r.Observation.Execution.Status = "canceled"
		}
	}
	// Cleanup is the one allowed deferred background context. It gets its own
	// deadline and owns every process. Cancellation never abandons join/cleanup.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), o.CleanupTimeout)
	defer cancel()
	rawPath := filepath.Join(workspace, "junit_01.xml")
	held, readErr := heldfile.OpenContext(cleanupCtx, rawPath, heldfile.Options{Label: "original conformance JUnit", MaxBytes: testresults.MaxBytes})
	if readErr == nil {
		r.rawJUnit, readErr = held.ReadAllContext(cleanupCtx, testresults.MaxBytes)
		_ = held.Close()
	}
	if readErr == nil {
		_, readErr = testresults.ParseJUnit(cleanupCtx, r.rawJUnit)
	}
	if readErr != nil {
		// An interrupted runner may not have downloaded an existing upstream file.
		// Retrieve it before resource deletion; never fabricate results from logs.
		fetched, _, fetchErr := a.kubeResults(cleanupCtx, workspace, "exec", "--namespace="+namespace, "e2e-conformance-test", "--container=output-container", "--", "cat", "/tmp/results/junit_01.xml")
		if fetchErr == nil {
			r.rawJUnit = fetched
			readErr = nil
		}
	}
	if readErr != nil && r.Observation.Execution.Status != "canceled" {
		r.Observation.Execution.Status = "infrastructure-error"
	}
	// Successful transport is not a usable suite result: validate even empty
	// fallback bytes, while preserving the original bytes and cancellation cause.
	report, parseErr := testresults.ParseJUnit(cleanupCtx, r.rawJUnit)
	if parseErr == nil && report.Summary.Total == 0 {
		parseErr = failure("upstream report contains zero cases")
	}
	if parseErr != nil && r.Observation.Execution.Status != "canceled" {
		r.Observation.Execution.Status = "infrastructure-error"
	}
	// Collect exact UIDs and verify that each upstream binding references this
	// fresh namespace before the wrapper performs any destructive operation.
	cleanupErr := a.cleanup(cleanupCtx, workspace, refs, owned, namespace)
	if cleanupErr != nil {
		r.Observation.Cleanup = testobservation.Outcome{Status: "failed", ExitCode: 1}
	}
	// Final passive observation has its own bounded recovery context; exhausted
	// deletion time never erases the independently observed post-run state.
	afterCtx, afterCancel := context.WithTimeout(context.Background(), o.CommandTimeout)
	defer afterCancel()
	after, afterErr := a.observe(afterCtx, workspace, "after")
	if afterErr == nil {
		r.Observation.Environment.Facts = append(r.Observation.Environment.Facts, after.Facts...)
	} else if r.Observation.Execution.Status == "completed" {
		r.Observation.Execution.Status = "infrastructure-error"
	}
	if readErr != nil {
		readErr = failure("original suite results unavailable")
	}
	// Preserve the runner cause first, including cancellation, while retaining
	// independent cleanup, post-observation and result-recovery failures.
	if err := errors.Join(runErr, cleanupErr, afterErr, parseErr, readErr); err != nil {
		return r, err
	}

	return r, nil
}

// Emit retains only the selected original JUnit and public environment facts.
// The caller must independently review that exact JUnit digest for publication;
// this digest declaration is disclosure consent, never signer or suite authority.
func (r *Result) Emit(ctx context.Context, out, reviewedJUnitDigest string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "conformance emission requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "conformance emission canceled before work")
	}
	if out == "" {
		return e.New(e.CodeUsage, "conformance output directory required")
	}

	if r == nil {
		return failure("conformance result unavailable")
	}
	if err := testobservation.ValidateDescription(ctx, r.Observation); err != nil {
		return err
	}
	if len(r.rawJUnit) > 0 && reviewedJUnitDigest != blobDigest(r.rawJUnit) {
		return failure("publication requires independently reviewed exact JUnit bytes")
	}
	source, err := os.MkdirTemp("", "nvkr-conformance-public-")
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating reviewed projection workspace")
	}
	defer os.RemoveAll(source)
	env, err := canonical.Marshal(r.Observation.Environment)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "encoding public environment")
	}
	if err = os.WriteFile(filepath.Join(source, "environment.json"), env, 0o600); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "writing public environment")
	}
	files := []testobservation.FileInput{{Name: "environment.json", Role: "environment", ExpectedDigest: blobDigest(env)}}
	if len(r.rawJUnit) > 0 {
		if err = os.WriteFile(filepath.Join(source, "junit_01.xml"), r.rawJUnit, 0o600); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "writing reviewed raw JUnit")
		}
		files = append(files, testobservation.FileInput{Name: "junit_01.xml", Role: "junit", ExpectedDigest: blobDigest(r.rawJUnit)})
	}
	return testobservation.Emit(ctx, source, r.Observation, files, out)
}
func blobDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type resourceRef struct{ path string }
type object struct {
	Metadata struct {
		UID string `json:"uid"`
	} `json:"metadata"`
	Subjects []struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		APIGroup  string `json:"apiGroup"`
	} `json:"subjects"`
	RoleRef struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		APIGroup string `json:"apiGroup"`
	} `json:"roleRef"`
}

func resourceRefs(ns string) []resourceRef {
	return []resourceRef{{"/api/v1/namespaces/" + ns}, {"/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/conformance-serviceaccount-role:" + ns}, {"/apis/rbac.authorization.k8s.io/v1/clusterroles/conformance-serviceaccount:" + ns}}
}
func (a *admitted) getObject(ctx context.Context, workspace string, ref resourceRef) (object, error) {
	// --ignore-not-found distinguishes absence from authentication/network errors
	// without interpreting localized stderr or accepting an arbitrary exit code.
	var kind, name string
	for i := len(ref.path) - 1; i >= 0; i-- {
		if ref.path[i] == '/' {
			name = ref.path[i+1:]
			prefix := ref.path[:i]
			for j := len(prefix) - 1; j >= 0; j-- {
				if prefix[j] == '/' {
					kind = prefix[j+1:]
					break
				}
			}
			break
		}
	}
	raw, _, err := a.kube(ctx, workspace, nil, "get", kind, name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return object{}, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return object{}, nil
	}
	var out object
	if err = decodeAPI(raw, &out); err != nil {
		return object{}, err
	}
	if out.Metadata.UID == "" {
		return object{}, failure("resource UID unavailable")
	}
	return out, nil
}
func (a *admitted) cleanup(ctx context.Context, workspace string, refs []resourceRef, owned map[string]string, namespace string) error {
	// Capture identities before deletion. A replaced namespace fails closed.
	for _, ref := range refs {
		found, err := a.getObject(ctx, workspace, ref)
		if err != nil {
			return err
		}
		if found.Metadata.UID == "" {
			continue
		}
		if uid := owned[ref.path]; uid != "" && uid != found.Metadata.UID {
			return failure("attempt namespace replaced; cleanup rejected")
		}
		if ref.path == refs[1].path {
			if len(found.Subjects) != 1 || found.Subjects[0].Namespace != namespace || found.Subjects[0].Name != "conformance-serviceaccount" || found.Subjects[0].Kind != "ServiceAccount" || found.Subjects[0].APIGroup != "" || found.RoleRef.Name != "conformance-serviceaccount:"+namespace || found.RoleRef.Kind != "ClusterRole" || found.RoleRef.APIGroup != "rbac.authorization.k8s.io" {
				return failure("attempt role binding has unrelated subjects")
			}
		}
		owned[ref.path] = found.Metadata.UID
	}
	// Delete cluster-scoped RBAC before namespace; each DELETE carries the UID
	// precondition authenticated by the API, avoiding GET/name-delete races.
	order := []resourceRef{refs[1], refs[2], refs[0]}
	var lastDeleteErr error
	for {
		for _, ref := range order {
			uid := owned[ref.path]
			if uid == "" {
				continue
			}
			found, err := a.getObject(ctx, workspace, ref)
			if err != nil {
				return err
			}
			if found.Metadata.UID == "" {
				continue
			}
			if found.Metadata.UID != uid {
				return failure("attempt resource replaced; cleanup rejected")
			}
			body, err := canonical.Marshal(struct {
				APIVersion    string `json:"apiVersion"`
				Kind          string `json:"kind"`
				Preconditions struct {
					UID string `json:"uid"`
				} `json:"preconditions"`
			}{APIVersion: "v1", Kind: "DeleteOptions", Preconditions: struct {
				UID string `json:"uid"`
			}{UID: uid}})
			if err != nil {
				return failure("encoding UID cleanup precondition")
			}
			if _, _, err = a.kube(ctx, workspace, bytes.NewReader(body), "delete", "--raw="+ref.path, "--filename=-"); err != nil {
				lastDeleteErr = err
				if ctx.Err() != nil {
					return err
				}
			}
		}
		allAbsent := true
		for _, ref := range refs {
			found, err := a.getObject(ctx, workspace, ref)
			if err != nil {
				return err
			}
			if found.Metadata.UID != "" {
				allAbsent = false
			}
		}
		if allAbsent {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for terminal cleanup"), lastDeleteErr)
		case <-timer.C:
		}
	}
}
