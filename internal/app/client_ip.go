package app

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

var cloudflareProxyCIDRs = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

type clientIPResolver struct {
	trustedProxyRanges []*net.IPNet
	cloudflareRanges   []*net.IPNet
}

func newClientIPResolver(cfg Config) (*clientIPResolver, error) {
	trustedProxyRanges, err := parseCIDRs(cfg.TrustedProxyCIDRs)
	if err != nil {
		return nil, err
	}

	var cloudflareRanges []*net.IPNet
	if cfg.TrustCloudflareIPs {
		cloudflareRanges, err = parseCIDRs(cloudflareProxyCIDRs)
		if err != nil {
			return nil, err
		}
	}

	return &clientIPResolver{
		trustedProxyRanges: trustedProxyRanges,
		cloudflareRanges:   cloudflareRanges,
	}, nil
}

func (r *clientIPResolver) clientIP(req *http.Request) string {
	remoteIP := parseRemoteIP(req.RemoteAddr)
	if remoteIP == nil {
		return clientKey(req)
	}
	if r.isCloudflareProxy(remoteIP) {
		if ip := parseHeaderIP(req.Header.Get("CF-Connecting-IP")); ip != nil {
			return ip.String()
		}
	}
	if r.isTrustedProxy(remoteIP) {
		if ip := parseHeaderIP(req.Header.Get("X-Real-IP")); ip != nil {
			return ip.String()
		}
		if ip := parseForwardedFor(req.Header.Get("X-Forwarded-For")); ip != nil {
			return ip.String()
		}
	}
	return remoteIP.String()
}

func (r *clientIPResolver) isTrustedProxy(ip net.IP) bool {
	return ipInRanges(ip, r.trustedProxyRanges)
}

func (r *clientIPResolver) isCloudflareProxy(ip net.IP) bool {
	return ipInRanges(ip, r.cloudflareRanges)
}

func ipInRanges(ip net.IP, ranges []*net.IPNet) bool {
	for _, ipNet := range ranges {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

func parseCIDRs(values []string) ([]*net.IPNet, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, ipNet, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy cidr %q: %w", value, err)
		}
		out = append(out, ipNet)
	}
	return out, nil
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(remoteAddr)
}

func parseHeaderIP(value string) net.IP {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return net.ParseIP(value)
}

func parseForwardedFor(value string) net.IP {
	for _, part := range strings.Split(value, ",") {
		if ip := parseHeaderIP(part); ip != nil {
			return ip
		}
	}
	return nil
}
