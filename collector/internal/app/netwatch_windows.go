//go:build windows

package app

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// netBuf is the last GetAdaptersAddresses buffer size that fitted.
var netBuf uint32 = 16 << 10

// netAddrs is the machine's network identity: every unicast address of
// every adapter that is up, loopback and link-local excepted, sorted. One
// GetAdaptersAddresses call per sample (net.Interfaces and Addrs make one
// per adapter, and Windows often has ten or more: Hyper-V, WSL, VPNs).
func netAddrs() (string, error) {
	const flags = windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST |
		windows.GAA_FLAG_SKIP_DNS_SERVER | windows.GAA_FLAG_SKIP_FRIENDLY_NAME
	size := netBuf
	var b []byte
	for range 3 {
		b = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&b[0])), &size)
		if err == nil {
			netBuf = max(netBuf, size)
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) || size <= uint32(len(b)) {
			return "", os.NewSyscallError("getadaptersaddresses", err)
		}
		b = nil // adapters added between the calls: try the larger size
	}
	if b == nil {
		return "", errors.New("getadaptersaddresses: buffer keeps growing")
	}
	var out []string
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&b[0])); aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp || aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		name := windows.BytePtrToString(aa.AdapterName)
		for u := aa.FirstUnicastAddress; u != nil; u = u.Next {
			ip := u.Address.IP()
			if ip == nil || ip.IsLinkLocalUnicast() || ip.IsLoopback() {
				continue
			}
			out = append(out, netKey(name, ip, int(u.OnLinkPrefixLength)))
		}
	}
	return netJoin(out), nil
}
