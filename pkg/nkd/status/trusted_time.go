package status

import (
	"context"
	"reflect"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

const trustedTimeSchemaVersion = "nkd.dev/status-trusted-time/v1"

// EpochSource is the narrow adapter owned by a release policy's trusted clock.
// It returns the policy epoch used by NKD lifecycle documents, not wall-clock
// time interpreted by this package.
type EpochSource interface {
	CurrentEpoch(context.Context) (int64, error)
}

// EpochSourceFunc adapts a context-aware epoch function to EpochSource.
type EpochSourceFunc func(context.Context) (int64, error)

func (source EpochSourceFunc) CurrentEpoch(ctx context.Context) (int64, error) {
	return source(ctx)
}

// TimeAuthority is a policy-owned capability that can observe the current
// lifecycle epoch. Its state is private so a caller-selected scalar cannot be
// mistaken for release-grade freshness authority.
type TimeAuthority struct {
	valid    bool
	identity string
	source   EpochSource
}

// NewTimeAuthority pins a canonical clock identity to its trusted adapter. The
// caller configuring release policy is responsible for selecting that adapter;
// downstream release requests receive only TrustedTime observations.
func NewTimeAuthority(identity string, source EpochSource) (TimeAuthority, error) {
	if strings.TrimSpace(identity) == "" || strings.TrimSpace(identity) != identity {
		return TimeAuthority{}, e.New(e.CodeTrustPolicyMissing,
			"status time authority requires a canonical identity")
	}
	if nilEpochSource(source) {
		return TimeAuthority{}, e.New(e.CodeTrustPolicyMissing,
			"status time authority requires an epoch source")
	}
	return TimeAuthority{valid: true, identity: identity, source: source}, nil
}

type trustedTimeCore struct {
	SchemaVersion string `json:"schemaVersion"`
	Authority     string `json:"authority"`
	Epoch         int64  `json:"epoch"`
}

// TrustedTime is the opaque, immutable observation returned by TimeAuthority.
// Its zero value and caller-authored AsOf values cannot authorize release.
type TrustedTime struct {
	valid     bool
	authority string
	epoch     int64
	binding   string
}

// Now snapshots the trusted source exactly once and binds the resulting epoch to
// the configured authority identity under a distinct canonical family.
func (authority TimeAuthority) Now(ctx context.Context) (TrustedTime, error) {
	if err := statusContextErr(ctx); err != nil {
		return TrustedTime{}, err
	}
	if !authority.valid || strings.TrimSpace(authority.identity) == "" ||
		strings.TrimSpace(authority.identity) != authority.identity || nilEpochSource(authority.source) {
		return TrustedTime{}, e.New(e.CodeTrustPolicyMissing,
			"status time authority is absent or invalid")
	}
	epoch, err := authority.source.CurrentEpoch(ctx)
	if err != nil {
		if ctxErr := statusContextErr(ctx); ctxErr != nil {
			return TrustedTime{}, ctxErr
		}
		return TrustedTime{}, e.Wrap(e.CodeStatusCheckpoint, err,
			"observing trusted status epoch")
	}
	if err := statusContextErr(ctx); err != nil {
		return TrustedTime{}, err
	}
	if epoch <= 0 {
		return TrustedTime{}, e.New(e.CodeStatusCheckpoint,
			"trusted status epoch must be positive")
	}
	binding, err := trustedTimeIdentity(authority.identity, epoch)
	if err != nil {
		return TrustedTime{}, err
	}
	return TrustedTime{valid: true, authority: authority.identity, epoch: epoch,
		binding: binding}, nil
}

func nilEpochSource(source EpochSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (observation TrustedTime) validate() error {
	if !observation.valid || observation.epoch <= 0 ||
		strings.TrimSpace(observation.authority) == "" ||
		strings.TrimSpace(observation.authority) != observation.authority ||
		!isDigest(observation.binding) {
		return e.New(e.CodeStatusCheckpoint,
			"trusted status time observation is absent or invalid")
	}
	binding, err := trustedTimeIdentity(observation.authority, observation.epoch)
	if err != nil {
		return err
	}
	if binding != observation.binding {
		return e.New(e.CodeStatusCheckpoint,
			"trusted status time observation changed after issuance")
	}
	return nil
}

func trustedTimeIdentity(authority string, epoch int64) (string, error) {
	return canonical.Digest(canonical.KindTrustedTime, trustedTimeCore{
		SchemaVersion: trustedTimeSchemaVersion,
		Authority:     authority,
		Epoch:         epoch,
	})
}

// VerifyTrustedTimeBinding semantically revalidates the serialized audit
// projection stored in an authenticated clearance. It does not mint TrustedTime
// authority; only TimeAuthority.Now can issue that opaque capability.
func VerifyTrustedTimeBinding(authority string, epoch int64, binding string) error {
	if strings.TrimSpace(authority) == "" || strings.TrimSpace(authority) != authority ||
		epoch <= 0 || !isDigest(binding) {
		return e.New(e.CodeStatusCheckpoint,
			"serialized trusted-time binding is incomplete")
	}
	want, err := trustedTimeIdentity(authority, epoch)
	if err != nil {
		return err
	}
	if binding != want {
		return e.New(e.CodeStatusCheckpoint,
			"serialized trusted-time binding does not match its v1 authority/epoch core")
	}
	return nil
}

func (observation TrustedTime) Epoch() (int64, error) {
	if err := observation.validate(); err != nil {
		return 0, err
	}
	return observation.epoch, nil
}

func (observation TrustedTime) Authority() (string, error) {
	if err := observation.validate(); err != nil {
		return "", err
	}
	return observation.authority, nil
}

func (observation TrustedTime) Binding() (string, error) {
	if err := observation.validate(); err != nil {
		return "", err
	}
	return observation.binding, nil
}
