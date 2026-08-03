package main

import "net"

// netInterfaceAddrs returns the useful local IP addresses (excluding loopback
// and link-local), in CIDR format.
func netInterfaceAddrs() ([]string, error) {
	var ifaces, err = net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out = []string{}
	var iface net.Interface
	for _, iface = range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		var addrs, addrErr = iface.Addrs()
		if addrErr != nil {
			continue
		}
		var addr net.Addr
		for _, addr = range addrs {
			var ipnet, ok = addr.(*net.IPNet)
			if !ok || ipnet.IP.IsLinkLocalUnicast() || ipnet.IP.IsLoopback() {
				continue
			}
			out = append(out, ipnet.String())
		}
	}
	return out, nil
}
