package gateway

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

var nonPublicIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// IsPublicHost matches the browser-side validation in
// apps/web/src/utils/webhook-public-base.ts.
func IsPublicHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(host)), ".")
	if host == "" {
		return false
	}
	if strings.Contains(host, ":") {
		return false
	}
	if public, ok := classifyDottedIPv4(host); ok {
		return public
	}
	if ip := net.ParseIP(host); ip != nil {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			return false
		}
		return isPublicIPv4(addr.Unmap())
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return isPublicIPv4(addr.Unmap())
	}
	if !strings.Contains(host, ".") {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if part == "" {
			return false
		}
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".test", ".invalid", ".example", ".home.arpa"} {
		if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

func classifyDottedIPv4(host string) (bool, bool) {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false, false
	}
	var decimalBytes [4]byte
	for i, part := range parts {
		if part == "" {
			return false, true
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return false, false
			}
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 255 {
			return false, true
		}
		if len(part) > 1 && strings.HasPrefix(part, "0") {
			return false, true
		}
		decimalBytes[i] = byte(value)
	}
	if !isPublicIPv4(netip.AddrFrom4(decimalBytes)) {
		return false, true
	}
	return true, true
}

func isPublicIPv4(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.Is4() {
		return false
	}
	for _, prefix := range nonPublicIPv4Prefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
