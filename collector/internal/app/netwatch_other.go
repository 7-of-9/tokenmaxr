//go:build !windows

package app

import "net"

// netAddrs is the machine's network identity: every address of every
// interface that is up, loopback and IPv6 link-local excepted, sorted.
func netAddrs() (string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var out []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return "", err
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() {
				continue // IPv6 link-local addresses come and go on their own
			}
			ones, _ := ipn.Mask.Size()
			out = append(out, netKey(ifc.Name, ipn.IP, ones))
		}
	}
	return netJoin(out), nil
}
