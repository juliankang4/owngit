package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"testing"
)

// routeLine is a /proc/net/route line as the kernel writes it, with the
// addresses in this computer's byte order.
func routeLine(iface, destination, gateway string, flags int) string {
	hex := func(address string) string {
		bytes := netip.MustParseAddr(address).As4()
		return fmt.Sprintf("%08X", binary.NativeEndian.Uint32(bytes[:]))
	}
	return fmt.Sprintf("%s\t%s\t%s\t%04X\t0\t0\t0\t%s\t0\t0\t0\n", iface, hex(destination), hex(gateway), flags, hex("0.0.0.0"))
}

func TestDefaultGatewayReadsTheDefaultRoute(t *testing.T) {
	header := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"
	table := header +
		routeLine("eth0", "172.18.0.0", "0.0.0.0", 0x1) +
		routeLine("eth1", "0.0.0.0", "0.0.0.0", 0x1) +
		routeLine("eth0", "0.0.0.0", "172.18.0.1", 0x3)
	gateway, err := defaultGateway([]byte(table))
	if err != nil || gateway != netip.MustParseAddr("172.18.0.1") {
		t.Fatalf("defaultGateway = %v, %v; want 172.18.0.1", gateway, err)
	}
	// A container without a network, or with only a route that needs no
	// gateway, has no address of the computer that runs it.
	for _, table := range []string{header, header + routeLine("eth0", "0.0.0.0", "0.0.0.0", 0x1), ""} {
		if gateway, err := defaultGateway([]byte(table)); err == nil {
			t.Errorf("table %q gave gateway %v", table, gateway)
		}
	}
}
