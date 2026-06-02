package forge

import (
	"errors"
	"testing"
)

func TestErrNotImplementedIsSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("ctx"), ErrNotImplemented)
	if !errors.Is(wrapped, ErrNotImplemented) {
		t.Fatal("ErrNotImplemented must be matchable with errors.Is")
	}
}
