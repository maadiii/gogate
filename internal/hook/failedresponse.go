package hook

import (
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

// failedResponseKey is the RequestContext key holding the response that was
// discarded on this request. It is deliberately package-private: hooks reach
// the value through FailedResponseFrom rather than by knowing the key, so the
// key can be changed without touching a single hook.
const failedResponseKey = "internal_failed_response"

// FailedResponse is a self-contained copy of what the downstream service sent,
// taken at the moment that response had to be thrown away.
//
// Everything here is plain Go data - copied bytes, a copied header map - and
// deliberately not a Hertz response. The response being discarded is about to
// be reset and its buffers handed back to a pool, so anything still pointing
// into it would be reading recycled memory.
type FailedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// CaptureFailedResponse copies the current response into rc so that hooks in
// the OnError stage can still see what the downstream service actually said,
// before the caller resets that response away.
//
// The caller decides *when* to call this, and that decision cannot be made by
// looking at the response. Hertz reports 200 for a response whose status was
// never set (ResponseHeader.StatusCode returns StatusOK when its internal
// status is zero), so "nothing was written" and "the backend answered 200" are
// the same value here. Only the code that knows whether the downstream call
// actually produced a response can tell them apart - on a proxy failure none
// did, and capturing there would tell an OnError hook the backend said 200
// when in fact it never answered at all.
func CaptureFailedResponse(rc *app.RequestContext) {
	header := make(http.Header)
	rc.Response.Header.VisitAll(func(key, value []byte) {
		// VisitAll's contract forbids retaining key or value after it
		// returns, so both are copied into the map rather than referenced.
		header.Add(string(key), string(value))
	})

	rc.Set(failedResponseKey, &FailedResponse{
		StatusCode: rc.Response.StatusCode(),
		Header:     header,
		Body:       append([]byte(nil), rc.Response.Body()...),
	})
}

// FailedResponseFrom returns the downstream response that was discarded on this
// request, and whether one was captured at all. A request whose backend never
// answered has no captured response - the original error for that case is the
// one recorded via app.RequestContext.Error.
func FailedResponseFrom(rc *app.RequestContext) (*FailedResponse, bool) {
	value, exists := rc.Get(failedResponseKey)
	if !exists {
		return nil, false
	}

	failed, ok := value.(*FailedResponse)
	if !ok {
		return nil, false
	}

	return failed, true
}
