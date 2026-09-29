package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"owngit/internal/service"
)

// inContainerImage reports whether this program is the one in the OwnGit
// container image.
func inContainerImage() bool {
	return detectInstall().Route == service.RouteContainer
}

// routeTable is Linux's IPv4 routing table; tests replace it.
var routeTable = "/proc/net/route"

// containerGateway returns the default IPv4 gateway of this container: the
// address of the computer that runs it on the container's network, from
// which Docker forwards that computer's connections to a published port.
func containerGateway() (netip.Addr, error) {
	table, err := os.ReadFile(routeTable)
	if err != nil {
		return netip.Addr{}, err
	}
	return defaultGateway(table)
}

// defaultGateway reads the gateway of the default route from the contents
// of /proc/net/route. The kernel writes each address as the hexadecimal
// value of its bytes in this computer's byte order.
func defaultGateway(table []byte) (netip.Addr, error) {
	const routeUsesGateway = 0x2 // RTF_GATEWAY
	lines := bufio.NewScanner(bytes.NewReader(table))
	lines.Scan() // the header
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, flagsErr := strconv.ParseUint(fields[3], 16, 16)
		gateway, gatewayErr := strconv.ParseUint(fields[2], 16, 32)
		if flagsErr != nil || gatewayErr != nil || flags&routeUsesGateway == 0 {
			continue
		}
		var address [4]byte
		binary.NativeEndian.PutUint32(address[:], uint32(gateway))
		return netip.AddrFrom4(address), nil
	}
	if err := lines.Err(); err != nil {
		return netip.Addr{}, err
	}
	return netip.Addr{}, errors.New("no default route through a gateway")
}
