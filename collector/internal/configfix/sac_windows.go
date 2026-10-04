//go:build windows

package configfix

import "golang.org/x/sys/windows/registry"

// SmartAppControl reports Windows Smart App Control, which can block
// unsigned binaries such as the collector.
func SmartAppControl() (state, detail string, ok bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\CI\Policy`, registry.QUERY_VALUE)
	if err != nil {
		return "unknown", "policy key is not readable", true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("VerifiedAndReputablePolicyState")
	if err != nil {
		return "off", "", true
	}
	switch v {
	case 0:
		return "off", "", true
	case 1:
		return "on", "enforced: unsigned collector builds may be blocked", true
	case 2:
		return "on", "evaluation mode: Windows may switch it to enforced", true
	}
	return "unknown", "", true
}
