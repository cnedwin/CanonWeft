package research

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"novelstudio/internal/skills"
)

var nextPublicRequest time.Time
var publicBackoff = map[string]time.Time{}

func publicClient(transport http.RoundTripper, host string) *http.Client {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			h, port, err := net.SplitHostPort(address)
			if err != nil || h != host || port != "443" {
				return nil, ErrSource
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
			if err != nil || len(ips) == 0 {
				return nil, ErrSource
			}
			for _, ip := range ips {
				if !skills.PublicAddress(ip) {
					return nil, ErrSource
				}
			}
			return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		}
		transport = t
	}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrSource }}
}

// Callers construct fixed-host HTTPS endpoints. No caller-supplied URL is fetched.
// Reuse the existing global lane/backoff across all public research sources.
func publicRead(ctx context.Context, client *http.Client, endpoint, accept string) ([]byte, error) {
	raw, _, err := publicResponse(ctx, client, endpoint, accept)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) {
		return nil, ErrSource
	}
	return raw, nil
}

type publicStatusError int

func (e publicStatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", ErrSource, int(e)) }
func (e publicStatusError) Unwrap() error { return ErrSource }

func publicResponse(ctx context.Context, client *http.Client, endpoint, accept string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !approvedPublicEndpoint(parsed) {
		return nil, nil, ErrSource
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	paceMu.Lock()
	coolingDown := publicBackoff[parsed.Host].After(time.Now())
	paceMu.Unlock()
	if coolingDown {
		return nil, nil, fmt.Errorf("%w: source cooling down", ErrSource)
	}
	select {
	case networkLane <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	defer func() { <-networkLane }()
	paceMu.Lock()
	coolingDown = publicBackoff[parsed.Host].After(time.Now())
	delay := time.Until(nextPublicRequest)
	paceMu.Unlock()
	if coolingDown {
		return nil, nil, fmt.Errorf("%w: source cooling down", ErrSource)
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, ErrSource
	}
	req.Header.Set("User-Agent", "NovelStudio-Research/1.0 (bounded author reference collector)")
	req.Header.Set("Accept", accept)
	response, err := client.Do(req)
	paceMu.Lock()
	nextPublicRequest = time.Now().Add(time.Second)
	paceMu.Unlock()
	if err != nil {
		return nil, nil, ErrSource
	}
	defer response.Body.Close()
	if response.StatusCode == 429 || response.StatusCode == 503 {
		backoff := time.Minute
		if n, e := strconv.Atoi(response.Header.Get("Retry-After")); e == nil && n > 60 && n < 86400 {
			backoff = time.Duration(n) * time.Second
		}
		paceMu.Lock()
		publicBackoff[parsed.Host] = time.Now().Add(backoff)
		paceMu.Unlock()
	}
	if response.StatusCode != 200 {
		return nil, nil, publicStatusError(response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 {
		return nil, nil, ErrSource
	}
	return raw, response.Header, nil
}
