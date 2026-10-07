package factory

import (
	"context"
	"errors"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPublicImagePullErrorClassification(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(t.Context())
			}
			defer cancel()
			failure := errors.New("ordinary pull failure")
			var active context.Context
			err := pullGitHubPublicImage(ctx, func(pullCtx context.Context) error {
				active = pullCtx
				if _, ok := pullCtx.Deadline(); !ok {
					t.Fatal("pull has no deadline")
				}
				switch mode {
				case "success":
					return nil
				case "failure":
					return failure
				case "cancel":
					cancel()
				}
				<-pullCtx.Done()
				return pullCtx.Err()
			})
			switch mode {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "failure":
				assertFactoryErrorCode(t, err, e.CodeFetchFailed)
				if !errors.Is(err, failure) {
					t.Fatalf("lost fetch cause: %v", err)
				}
			default:
				assertFactoryErrorCode(t, err, e.CodeTimeout)
				if !errors.Is(err, ctx.Err()) {
					t.Fatalf("lost interruption cause: %v", err)
				}
			}
			if active.Err() == nil {
				t.Fatal("pull context survived operation")
			}
		})
	}
}
