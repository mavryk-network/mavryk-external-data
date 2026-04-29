package httpclient

import (
	"errors"
	"io"
	"net/http"
)

// ErrResponseTooLarge is returned by the limited-body reader when an outbound
// HTTP response exceeds the configured size cap.
var ErrResponseTooLarge = errors.New("outbound HTTP response too large")

// MaxBytesReader wraps next with a transport that bounds the size of any
// response body to maxBytes (post-decompression). 0 disables the cap.
//
// We don't trust upstream Content-Length — it can lie, be missing, or arrive
// after a chunked-encoding transport finishes. The limited reader enforces the
// cap on the actual bytes consumed.
func MaxBytesReader(next http.RoundTripper, maxBytes int64) http.RoundTripper {
	if maxBytes <= 0 {
		return next
	}
	if next == nil {
		next = http.DefaultTransport
	}
	return &maxBytesTransport{next: next, maxBytes: maxBytes}
}

type maxBytesTransport struct {
	next     http.RoundTripper
	maxBytes int64
}

func (t *maxBytesTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedReadCloser{
		body: resp.Body,
		left: t.maxBytes,
	}
	return resp, nil
}

type limitedReadCloser struct {
	body io.ReadCloser
	left int64
}

func (l *limitedReadCloser) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.body.Read(p)
	l.left -= int64(n)
	if err == nil && l.left <= 0 {
		return n, ErrResponseTooLarge
	}
	return n, err
}

func (l *limitedReadCloser) Close() error { return l.body.Close() }
