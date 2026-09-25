package requestctx

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// Minimum prefix lengths of a trusted proxy range. A proxy lives on one
// computer or one private network, and the widest private IPv4 range,
// 10.0.0.0/8, is a /8; an IPv6 /32 is already a whole provider allocation.
// Wider ranges would trust most of the Internet, so they are refused.
const (
	minimumIPv4ProxyBits = 8
	minimumIPv6ProxyBits = 32
)

// ParseTrustedProxy parses one trusted proxy: an IP address, such as
// 192.0.2.10 or fd00::10, or a CIDR range, such as 172.18.0.0/16. It refuses
// ranges wider than /8 for IPv4 or /32 for IPv6 (so also 0.0.0.0/0 and
// ::/0), ranges and addresses made of the unspecified address (0.0.0.0/32,
// ::/128), IPv6 zones, IPv4-mapped IPv6 ranges and ranges with bits set
// after the prefix length. An IPv4-mapped IPv6 address becomes its IPv4
// form.
func ParseTrustedProxy(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("trusted proxy %q must be an IP address or a CIDR range", value)
		}
		minimum := minimumIPv4ProxyBits
		if prefix.Addr().Is6() {
			minimum = minimumIPv6ProxyBits
		}
		switch {
		case prefix.Addr().Is4In6():
			return netip.Prefix{}, fmt.Errorf("trusted proxy %q is an IPv4-mapped IPv6 range; write the IPv4 range instead", value)
		case prefix.Bits() < minimum:
			return netip.Prefix{}, fmt.Errorf("trusted proxy %q covers too many addresses; name the proxy's address or its network, no wider than /%d", value, minimum)
		case prefix != prefix.Masked():
			return netip.Prefix{}, fmt.Errorf("trusted proxy %q has address bits set after the prefix length; use %s or a single address", value, prefix.Masked())
		case prefix.Addr().IsUnspecified():
			return netip.Prefix{}, fmt.Errorf("trusted proxy %q starts at the unspecified address; name the proxy's own address or network", value)
		}
		return prefix, nil
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("trusted proxy %q must be an IP address or a CIDR range; host names are not accepted", value)
	}
	if address.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("trusted proxy %q has an IPv6 zone; remove the part from %%", value)
	}
	address = address.Unmap()
	if address.IsUnspecified() {
		return netip.Prefix{}, fmt.Errorf("trusted proxy %q is the unspecified address; name the proxy's own address", value)
	}
	return netip.PrefixFrom(address, address.BitLen()), nil
}

// FormatTrustedProxy is the canonical text of a parsed trusted proxy: the
// address alone for a single address, otherwise the CIDR range.
func FormatTrustedProxy(prefix netip.Prefix) string {
	if prefix.IsSingleIP() {
		return prefix.Addr().String()
	}
	return prefix.String()
}

// trusts reports whether peer, a connection's remote address, is a trusted
// proxy.
func (resolver Resolver) trusts(peer string) bool {
	if len(resolver.TrustedProxies) == 0 {
		return false
	}
	address, ok := peerAddress(peer)
	if !ok {
		return false
	}
	for _, prefix := range resolver.TrustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// peerAddress parses a remote address with or without a port, without its
// IPv6 zone and in IPv4 form when it is IPv4-mapped.
func peerAddress(peer string) (netip.Addr, bool) {
	address, err := netip.ParseAddr(peer)
	if err != nil {
		addressPort, portErr := netip.ParseAddrPort(peer)
		if portErr != nil {
			return netip.Addr{}, false
		}
		address = addressPort.Addr()
	}
	return address.WithZone("").Unmap(), true
}

// singleValue returns the value of a header sent exactly once with one
// nonempty value, not a comma-separated list.
func singleValue(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	if value == "" || strings.Contains(value, ",") {
		return "", false
	}
	return value, true
}

// lastForwardedFor returns the rightmost X-Forwarded-For entry, which the
// trusted proxy appended, when it is an IP address. Repeated header lines
// form one list in order.
func lastForwardedFor(header http.Header) (string, bool) {
	values := header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return "", false
	}
	list := strings.Join(values, ",")
	address, err := netip.ParseAddr(strings.TrimSpace(list[strings.LastIndex(list, ",")+1:]))
	if err != nil {
		return "", false
	}
	return address.WithZone("").Unmap().String(), true
}
