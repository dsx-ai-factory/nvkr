package release

import (
	"context"
	"reflect"
	"sort"
	"strings"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/status"
)

// TagResolver resolves a mutable "name:tag" reference to its current immutable digest
// (e.g. a registry manifest lookup). It is the ONLY place a tag is touched.
type TagResolver func(ref string) (digest string, err error)

// ResolveSelector is the single digest-first resolve choke-point (ADR-16 §3): a
// "name@sha256:…" reference is used directly; a "name:tag" is resolved to its current
// digest and the RESOLVED DIGEST becomes the effective selection (the tag is never
// persisted as identity). A compromised registry therefore cannot let a mutable tag
// bypass the later digest+signature verification.
func ResolveSelector(ref string, resolve TagResolver) (digest string, err error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" || trimmed != ref {
		return "", e.New(e.CodeUnverifiedRef,
			"release selector is empty or has surrounding whitespace")
	}
	if i := strings.Index(ref, "@sha256:"); i >= 0 {
		d := ref[i+1:]
		if i == 0 || !isDigest(d) {
			return "", e.New(e.CodeUnverifiedRef, "malformed digest in selector %q", ref)
		}
		return d, nil
	}
	if resolve == nil {
		return "", e.New(e.CodeUnverifiedRef,
			"selector %q is a mutable tag but no resolver was supplied (select by @sha256: or provide a resolver)", ref)
	}
	d, err := resolve(ref)
	if err != nil {
		return "", e.Wrap(e.CodeUnverifiedRef, err, "resolving tag %q to a digest", ref)
	}
	if !isDigest(d) {
		return "", e.New(e.CodeUnverifiedRef, "tag %q resolved to a malformed digest %q", ref, d)
	}
	return d, nil
}

// IncrementalPlan is a diagnostic candidate-diff helper. It splits next's members
// into changed and byte-identical declarations, but it does not authenticate prior
// evidence, issue a reuse capability, or bypass Authorize. A production evidence
// cache must start from an authenticated prior clearance and revalidate current
// policy, status, time, and receipt applicability before skipping work (ADR-16 §4).
func IncrementalPlan(prev, next *ReleaseSet) (revalidate, reused []Member) {
	prior := map[string]Member{}
	counts := map[string]int{}
	if prev != nil {
		for _, m := range prev.Members {
			k := memberKey(m)
			counts[k]++
			prior[k] = normalizedMember(m)
		}
	}
	for _, m := range next.sorted().Members {
		k := memberKey(m)
		if p, ok := prior[k]; ok && counts[k] == 1 && reflect.DeepEqual(p, normalizedMember(m)) {
			reused = append(reused, m)
			continue
		}
		revalidate = append(revalidate, m)
	}
	return revalidate, reused
}

// RollbackRequest carries the fresh, authenticated, anti-rollback-checked status
// checkpoint needed to re-authorize a historical candidate. Candidates are searched
// by recomputed digest, never map key.
type RollbackRequest struct {
	TargetDigest string
	Candidates   []AuthorizationRequest
	Status       status.CheckpointReceipt
}

// RollbackAuthorization is the result of rerunning current release and status checks.
type RollbackAuthorization struct {
	set           ReleaseSet
	authorization *Authorization
	decision      DecisionSnapshot
}

// DecisionSnapshot records the current status outcome checked during rollback.
type DecisionSnapshot struct {
	Effective        bool
	Action           string
	ReplacedBy       string
	Warning          string
	CheckpointDigest string
	PolicyBinding    string
	AsOfEpoch        int64
}

// ReleaseSet returns a defensive copy of the re-authorized set.
func (authorization *RollbackAuthorization) ReleaseSet() *ReleaseSet {
	if authorization == nil {
		return nil
	}
	return authorization.set.sorted()
}

// Clearance returns the private release authorization capability.
func (authorization *RollbackAuthorization) Clearance() *Authorization {
	if authorization == nil {
		return nil
	}
	return authorization.authorization
}

// Status returns the status decision evaluated during rollback.
func (authorization *RollbackAuthorization) Status() DecisionSnapshot {
	if authorization == nil {
		return DecisionSnapshot{}
	}
	return authorization.decision
}

// AuthorizeRollback recomputes each candidate identity and reruns current release
// clearance and status enforcement before returning a rollback capability.
func AuthorizeRollback(ctx context.Context, req RollbackRequest) (*RollbackAuthorization, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if !isDigest(req.TargetDigest) {
		return nil, e.New(e.CodeUnverifiedRef, "rollback target %q is not an immutable release-set digest", req.TargetDigest)
	}
	statusBinding, err := BindVerifiedStatusCheckpoint(req.Status)
	if err != nil {
		return nil, err
	}
	decision, err := req.Status.Decision()
	if err != nil {
		return nil, err
	}
	for _, candidate := range req.Candidates {
		if !candidate.Release.valid {
			continue
		}
		recomputed, err := candidate.Release.set.Digest()
		if err != nil {
			return nil, err
		}
		if recomputed != candidate.Release.digest {
			return nil, e.New(e.CodeUnverifiedRef, "verified release receipt changed identity from %s to %s", candidate.Release.digest, recomputed)
		}
		if recomputed != req.TargetDigest {
			continue
		}
		publicationIdentity, err := candidate.Release.PublicationIdentity()
		if err != nil {
			return nil, err
		}
		if statusBinding.target != publicationIdentity {
			return nil, e.New(e.CodeStatusCheckpoint,
				"rollback status checkpoint targets %s instead of release publication %s",
				statusBinding.target, publicationIdentity)
		}
		candidate.Status = statusBinding
		candidate.AsOfEpoch = statusBinding.asOfEpoch
		clearance, err := Authorize(ctx, candidate)
		if err != nil {
			return nil, err
		}
		return &RollbackAuthorization{
			set: *candidate.Release.set.sorted(), authorization: clearance,
			decision: DecisionSnapshot{
				Effective: decision.Effective, Action: decision.Action, ReplacedBy: decision.ReplacedBy,
				Warning: decision.Warn, CheckpointDigest: statusBinding.checkpointDigest,
				PolicyBinding: statusBinding.policyBinding, AsOfEpoch: statusBinding.asOfEpoch,
			},
		}, nil
	}
	return nil, e.New(e.CodeUnverifiedRef, "rollback target %s is not among the recomputed candidate release-set digests", req.TargetDigest)
}

func normalizedMember(member Member) Member {
	member.ArtifactDigests = append([]string(nil), member.ArtifactDigests...)
	sort.Strings(member.ArtifactDigests)
	return member
}
