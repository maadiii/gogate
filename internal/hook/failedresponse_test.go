package hook_test

import (
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/maadiii/gogate/internal/hook"
)

func newContextWithResponse(method, path string) *app.RequestContext {
	return ut.CreateUtRequestContext(method, path, &ut.Body{})
}

// The capture exists so the OnError stage can report what the backend said
// after that payload has been taken away from the client. These tests pin the
// two properties that make it usable: the copy is faithful, and it is a copy -
// resetting the response afterwards must not empty it.
func TestCaptureFailedResponse_RoundTrip(t *testing.T) {
	t.Parallel()

	rc := newContextWithResponse("GET", "/ping")

	rc.Response.SetStatusCode(503)
	rc.Response.Header.Set("X-Backend", "yes")
	rc.Response.SetBodyString("service unavailable")

	hook.CaptureFailedResponse(rc)

	// Taking the response away is the whole point of the capture, so the
	// snapshot has to survive exactly this.
	rc.Response.Reset()

	failed, ok := hook.FailedResponseFrom(rc)
	if !ok {
		t.Fatal("expected a captured response, got none")
	}
	if failed.StatusCode != 503 {
		t.Errorf("expected captured status 503, got %d", failed.StatusCode)
	}
	if got := string(failed.Body); got != "service unavailable" {
		t.Errorf("expected captured body %q, got %q", "service unavailable", got)
	}
	if got := failed.Header.Get("X-Backend"); got != "yes" {
		t.Errorf("expected captured X-Backend header %q, got %q", "yes", got)
	}
}

// The snapshot must be plain copied data, not a view into the response. The
// response being discarded has its buffers pooled, so a snapshot still pointing
// into it would be reading memory another request had already been given.
func TestCaptureFailedResponse_IsDetachedFromTheResponse(t *testing.T) {
	t.Parallel()

	rc := newContextWithResponse("GET", "/ping")

	rc.Response.SetStatusCode(200)
	rc.Response.SetBodyString("original")

	hook.CaptureFailedResponse(rc)

	// Mutating the live response must not reach the snapshot.
	rc.Response.SetStatusCode(500)
	rc.Response.SetBodyString("overwritten")

	failed, ok := hook.FailedResponseFrom(rc)
	if !ok {
		t.Fatal("expected a captured response, got none")
	}
	if failed.StatusCode != 200 {
		t.Errorf("expected the snapshot to keep status 200, got %d", failed.StatusCode)
	}
	if got := string(failed.Body); got != "original" {
		t.Errorf("expected the snapshot to keep body %q, got %q", "original", got)
	}
}

// A backend can legitimately send the same header more than once (Set-Cookie
// being the usual case), so the capture must not collapse them to the last
// value.
func TestCaptureFailedResponse_PreservesRepeatedHeaders(t *testing.T) {
	t.Parallel()

	rc := newContextWithResponse("GET", "/ping")

	rc.Response.SetStatusCode(200)
	rc.Response.Header.Add("X-Multi", "first")
	rc.Response.Header.Add("X-Multi", "second")

	hook.CaptureFailedResponse(rc)

	failed, ok := hook.FailedResponseFrom(rc)
	if !ok {
		t.Fatal("expected a captured response, got none")
	}

	values := failed.Header.Values("X-Multi")
	if len(values) != 2 || values[0] != "first" || values[1] != "second" {
		t.Errorf("expected both header values [first second] in order, got %v", values)
	}
}

// CaptureFailedResponse deliberately does not try to guess whether the backend
// actually answered, because it cannot: Hertz reports 200 for a response whose
// status was never set, which makes "nothing was written" and "the backend
// answered 200" the same value. Deciding *when* to capture is therefore the
// caller's job, and this test pins the Hertz behaviour that forces that.
func TestCaptureFailedResponse_UsesHertzDefaultStatus(t *testing.T) {
	t.Parallel()

	rc := newContextWithResponse("GET", "/ping")

	if got := rc.Response.StatusCode(); got != http.StatusOK {
		t.Fatalf("expected Hertz to report 200 for a response that was never written, got %d", got)
	}

	hook.CaptureFailedResponse(rc)

	failed, ok := hook.FailedResponseFrom(rc)
	if !ok {
		t.Fatal("expected a captured response, got none")
	}
	if failed.StatusCode != http.StatusOK {
		t.Errorf("expected the captured status to be the 200 Hertz reports, got %d", failed.StatusCode)
	}
	if len(failed.Body) != 0 {
		t.Errorf("expected an empty captured body, got %q", failed.Body)
	}
}

func TestFailedResponseFrom_NothingCaptured(t *testing.T) {
	t.Parallel()

	rc := newContextWithResponse("GET", "/ping")

	failed, ok := hook.FailedResponseFrom(rc)
	if ok || failed != nil {
		t.Errorf("expected no captured response on a fresh context, got %+v (ok=%v)", failed, ok)
	}
}

// Contexts are pooled and reused by Hertz, so a value stashed on one request
// must not be readable from the next one. Hertz clears ctx.Keys on Reset, and
// this pins that the capture relies on it correctly.
func TestFailedResponseFrom_DoesNotSurviveContextReset(t *testing.T) {
	t.Parallel()

	rc := newContextWithResponse("GET", "/ping")
	rc.Response.SetStatusCode(200)
	rc.Response.SetBodyString("first request")

	hook.CaptureFailedResponse(rc)

	if _, ok := hook.FailedResponseFrom(rc); !ok {
		t.Fatal("expected a captured response before the reset, got none")
	}

	rc.Reset()

	if _, ok := hook.FailedResponseFrom(rc); ok {
		t.Error("expected the captured response to be gone after the context was reset, but it survived")
	}
}
