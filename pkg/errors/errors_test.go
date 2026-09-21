package errors_test

import (
	stderrors "errors"
	"fmt"
	"testing"

	"github.com/maadiii/gogate/pkg/errors"
)

// TestWrap_KeepsTheCause is the regression test for a wrapping bug that made
// every wrapped error a dead end: the original was rebuilt from its message
// string, so errors.Is could never reach it and the chain of what actually
// failed stopped one frame in.
func TestWrap_KeepsTheCause(t *testing.T) {
	t.Parallel()

	cause := stderrors.New("connection refused")

	wrapped := errors.Wrap(cause)
	if !stderrors.Is(wrapped, cause) {
		t.Error("expected errors.Is to reach the original error through Wrap")
	}

	if got := stderrors.Unwrap(wrapped); got == nil {
		t.Fatal("expected Wrap to produce an error that unwraps")
	}
}

func TestWrapCK_KeepsTheCause(t *testing.T) {
	t.Parallel()

	cause := stderrors.New("backend said no")

	wrapped := errors.WrapCK(cause, errors.CodeBadGateway, "bad gateway")
	if !stderrors.Is(wrapped, cause) {
		t.Error("expected errors.Is to reach the original error through WrapCK")
	}

	if got := errors.StatusOf(wrapped); got == 0 {
		t.Errorf("expected a status for a coded error, got %d", got)
	}
}

// TestWrap_LeavesACodedErrorAlone pins the rule the pipeline depends on: an
// error that already carries a code survives being wrapped, so a hook that
// deliberately rejects a request is not flattened into a 500 by the code that
// merely forwards its error.
func TestWrap_LeavesACodedErrorAlone(t *testing.T) {
	t.Parallel()

	original := errors.Unauthorized()

	if wrapped := errors.Wrap(original); wrapped != original {
		t.Error("expected Wrap to return an already-coded error untouched")
	}
}

// TestWrap_GivesAnUncodedErrorACode covers the other half of the rule: an error
// with nothing to say about itself is classified as internal, which is what
// makes a plain failure from a misbehaving hook a 500 rather than a 200.
func TestWrap_GivesAnUncodedErrorACode(t *testing.T) {
	t.Parallel()

	wrapped := errors.Wrap(stderrors.New("something broke"))
	if !errors.Is(wrapped, errors.Wrap(stderrors.New("something else"))) {
		t.Error("expected two uncoded errors to be classified as the same kind")
	}

	if got := errors.StatusOf(wrapped); got != 500 {
		t.Errorf("expected an uncoded error to map to 500, got %d", got)
	}
}

func TestWrap_NilStaysNil(t *testing.T) {
	t.Parallel()

	if got := errors.Wrap(nil); got != nil {
		t.Errorf("expected Wrap(nil) to be nil, got %v", got)
	}
}

// TestIs_MatchesByKind documents what Is compares: the code and the key, so
// that two independently built errors describing the same failure match.
func TestIs_MatchesByKind(t *testing.T) {
	t.Parallel()

	if !errors.Is(errors.Unauthorized(), errors.Unauthorized()) {
		t.Error("expected two Unauthorized() errors to match each other")
	}

	if errors.Is(errors.Unauthorized(), errors.Forbidden()) {
		t.Error("expected Unauthorized() and Forbidden() not to match")
	}
}

// TestIs_DoesNotTreatTheZeroValueAsAWildcard is the regression test for the bug
// that made the re-wrap guard a no-op. Comparing against a bare new(Error) used
// to be the idiom for "is this one of ours?", but the zero value has an empty
// key, so it answered false for every real error.
func TestIs_DoesNotTreatTheZeroValueAsAWildcard(t *testing.T) {
	t.Parallel()

	if errors.Is(errors.Unauthorized(), new(errors.Error)) {
		t.Error("expected a bare new(Error) not to match a coded error")
	}
}

// TestIs_FallsThroughForPlainErrors keeps ordinary sentinel comparisons working
// for callers that pass something this package never produced.
func TestIs_FallsThroughForPlainErrors(t *testing.T) {
	t.Parallel()

	sentinel := stderrors.New("sentinel")

	if !errors.Is(fmt.Errorf("wrapping: %w", sentinel), sentinel) {
		t.Error("expected a plain wrapped sentinel to fall through to errors.Is")
	}
}

func TestNew_IsInternalByDefault(t *testing.T) {
	t.Parallel()

	if got := errors.StatusOf(errors.New("boom")); got != 500 {
		t.Errorf("expected New to default to a 500, got %d", got)
	}
}
