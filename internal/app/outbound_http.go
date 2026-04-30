package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type safeOutboundDialer struct {
	allowPrivate func() bool
	resolver     ipResolver
	dialer       net.Dialer
}

func newOutboundHTTPClient(cfg *Config) *http.Client {
	timeout := 10 * time.Second
	if cfg != nil {
		timeout = minPositiveDuration(cfg.HTTPClientTimeout, timeout)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = safeOutboundDialer{
		allowPrivate: func() bool {
			return cfg != nil && cfg.AllowPrivateCallbacks
		},
		resolver: net.DefaultResolver,
		dialer: net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		},
	}.DialContext

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (d safeOutboundDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.allowPrivate != nil && d.allowPrivate() {
		return d.dialer.DialContext(ctx, network, address)
	}

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return nil, errors.New("private network callbacks are not allowed")
		}
		return d.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}

	resolver := d.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errors.New("callback host lookup failed")
	}
	for _, addr := range addrs {
		if isPrivateIP(addr.IP) {
			return nil, errors.New("private network callbacks are not allowed")
		}
	}

	var lastErr error
	for _, addr := range addrs {
		conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("callback host lookup failed")
}

func minPositiveDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
