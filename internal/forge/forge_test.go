package forge

import (
	"context"
	"errors"
	"testing"
)

func TestErrNotImplementedIsSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("ctx"), ErrNotImplemented)
	if !errors.Is(wrapped, ErrNotImplemented) {
		t.Fatal("ErrNotImplemented must be matchable with errors.Is")
	}
}

// compile-time guard: a minimal stub must satisfy the refined CodeForge.
type shapeStub struct{}

func (shapeStub) EnsureClone(ctx context.Context, repo string) (string, error) { return "", nil }
func (shapeStub) CreateWorktree(ctx context.Context, repo, branch string) (string, error) {
	return "", nil
}
func (shapeStub) RemoveWorktree(ctx context.Context, repo, path string) error { return nil }
func (shapeStub) PushBranch(ctx context.Context, repo, branch string) error   { return nil }
func (shapeStub) OpenPR(ctx context.Context, repo, branch, base, title, body string) (string, int, error) {
	return "", 0, nil
}
func (shapeStub) PRStatus(ctx context.Context, repo string, prNumber int) (PRStatus, error) {
	return PRStatus{}, nil
}
func (shapeStub) CreateWorktreeFromBranch(ctx context.Context, repo, branch string) (string, error) {
	return "", nil
}
func (shapeStub) PRReviewFeedback(ctx context.Context, repo string, prNumber int) (ReviewFeedback, error) {
	return ReviewFeedback{}, nil
}
func (shapeStub) CheckAnnotations(ctx context.Context, repo string, prNumber int) ([]CheckAnnotation, error) {
	return nil, nil
}

func TestCodeForgeShape(t *testing.T) {
	var _ CodeForge = shapeStub{}
}
