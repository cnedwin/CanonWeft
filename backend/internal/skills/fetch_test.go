package skills

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type fetchTransport struct{ calls int }

func (t *fetchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1/secret"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}
func TestRedirectToPrivateIPRejected(t *testing.T) {
	transport := &fetchTransport{}
	f := NewFetcher(transport)
	if _, _, err := f.Fetch(context.Background(), RemoteRequest{Repository: "org/repo", Ref: "main", Path: "skills/test"}); err == nil || transport.calls != 1 {
		t.Fatal("followed redirect", transport.calls, err)
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:192.168.1.2", "100.64.0.1", "198.18.0.1"} {
		if PublicAddress(netip.MustParseAddr(ip)) {
			t.Fatal("private/reserved address allowed", ip)
		}
	}
	before := transport.calls
	if _, _, err := f.Fetch(context.Background(), RemoteRequest{Repository: "https://127.0.0.1/x", Path: "test"}); err == nil || transport.calls != before {
		t.Fatal("arbitrary URL accepted")
	}
}
