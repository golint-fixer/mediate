// Package mediate provides retryable, failure
// tolerant and rate limited HTTP Transport / RoundTripper interfaces
// for all net.Http client users.
package mediate

import (
	"net/http"
)

// cloneRequest returns a clone of the provided *http.Request.
// The clone is a shallow copy of the struct. Bodies
// should be deep copied here due to closing.
func cloneRequest(r *http.Request) *http.Request {
	// shallow copy of the struct
	r2 := new(http.Request)
	*r2 = *r
	return r2
}

type canceler interface {
	CancelRequest(*http.Request)
}

type fixedRetries struct {
	underlying http.RoundTripper
	retriesAllowed int
}

// Build a new FixedRetry transport - on any failure, the request will be retried
// at most count times.
func FixedRetries(count int, transport http.RoundTripper) http.RoundTripper {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &fixedRetries{underlying: transport, retriesAllowed: count}
}

func (t *fixedRetries) CancelRequest(req *http.Request) {
	tr, ok := t.underlying.(canceler)
	if ok {
		tr.CancelRequest(req)
	}
}

func (t *fixedRetries) RoundTrip(req *http.Request) (*http.Response, error) {
	var lastError error
	for retry := 0; retry < t.retriesAllowed; retry++ {
		nreq := cloneRequest(req)
		resp, lastError := t.underlying.RoundTrip(nreq)
		if lastError == nil {
			return resp, nil
		}
	}
	return nil, lastError
}
