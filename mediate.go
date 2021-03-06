// Package mediate provides retryable, failure
// tolerant and rate limited HTTP Transport / RoundTripper interfaces
// for all net.Http client users.
package mediate

import (
	"bytes"
	"errors"
	"io/ioutil"
	"net/http"
	"time"
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

// cloneResponse makes a new shallow clone of an http.Response
func cloneResponse(r *http.Response) *http.Response {
	// shallow copy of the struct
	r2 := new(http.Response)
	*r2 = *r
	return r2
}

type canceler interface {
	CancelRequest(*http.Request)
}

// FixedRetry transport - on any failure, the request will be retried
// at most count times.
type fixedRetries struct {
	transport      http.RoundTripper
	retriesAllowed int
}

// FixedRetries will issue the same request up to count times, if
// an explicit error (socket error, transport error) is returned
// from the underlying RoundTripper. This implementation performs
// no backoff and does not look at the http.Response status codes.
func FixedRetries(count int, transport http.RoundTripper) http.RoundTripper {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &fixedRetries{transport: transport, retriesAllowed: count}
}

func (t *fixedRetries) CancelRequest(req *http.Request) {
	tr, ok := t.transport.(canceler)
	if ok {
		tr.CancelRequest(req)
	}
}

func (t *fixedRetries) RoundTrip(req *http.Request) (*http.Response, error) {
	var lastError error
	for retry := 0; retry < t.retriesAllowed; retry++ {
		nreq := cloneRequest(req)
		var resp *http.Response
		resp, lastError = t.transport.RoundTrip(nreq)
		if lastError == nil {
			return resp, nil
		}
	}
	return nil, lastError
}

/////////////////////////

type reliableBody struct {
	transport http.RoundTripper
}

// ReliableBody builds a RoundTripper which will consume all
// of the response Body into a new memory buffer, and returns
// the response with this alternate Body.
//
// This is less memory efficient compared to streaming the response
// from the socket directly, but allows API to work with complete
// operations making retries and other actions trivial.
func ReliableBody(transport http.RoundTripper) http.RoundTripper {
	return &reliableBody{transport}
}

func (t *reliableBody) CancelRequest(req *http.Request) {
	tr, ok := t.transport.(canceler)
	if ok {
		tr.CancelRequest(req)
	}
}

func (t *reliableBody) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err != nil {
		return nil, err
	}
	buf := bytes.NewReader(body)
	resp.Body = ioutil.NopCloser(buf)
	return resp, nil
}

/////////////////////////

type rateLimit struct {
	requests  int
	quantum   time.Duration
	transport http.RoundTripper
	limiter   chan bool
	done      chan bool
}

// RateLimit builds a RoundTripper which will permit up to
// requests through every "every" duration to the passed transport.
// Requests will be blocked on a channel receive. Currently, RateLimit
// will split the internal into 10 quantums, and permit an integer
// number of requests through in that interval.
//
// If there are less than 10 requests, the quantum will be made to match
// the given interval.
func RateLimit(requests int, every time.Duration, transport http.RoundTripper) http.RoundTripper {
	div := 10
	if requests < div {
		div = 1
	}
	q := time.Duration(int64(every) / int64(div))

	rl := &rateLimit{requests: requests / div,
		quantum: q, transport: transport,
		limiter: make(chan bool),
		done:    make(chan bool)}

	go rl.ticker()
	return rl
}

func (r *rateLimit) ticker() {
	tick := time.NewTicker(r.quantum)
	defer tick.Stop()
	defer close(r.limiter)

	limit := r.requests
	for {
		select {
		case _, ok := <-r.done:
			if !ok {
				return
			}
		case r.limiter <- true:
			limit--
			// Allow a request
		case _ = <-tick.C:
			// Expired? reset the counter
			limit = r.requests
		}
		// Out of tokens? Wait until the timer expires
		if limit <= 0 {
			_ = <-tick.C
			limit = r.requests
		}
	}
}

func (r *rateLimit) RoundTrip(req *http.Request) (*http.Response, error) {
	_, ok := <-r.limiter
	if !ok {
		return nil, errors.New("This rate limited transport has been closed.")
	}
	return r.transport.RoundTrip(req)
}

func (r *rateLimit) Close() {
	// Close the done channel so the ticker picks it up
	close(r.done)
}
