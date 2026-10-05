//go:build windows

package homes

import (
	"context"
	"errors"

	"golang.org/x/sys/windows/registry"
)

// DefaultCodexLookups are the places beyond this process's environment that
// can set CODEX_HOME on Windows: the user's and the machine's environment in
// the registry. A process started before the variable was set (the
// collector's autostart entry, an app launched from an old Explorer) does
// not see either until it restarts.
func DefaultCodexLookups() []CodexLookup {
	return []CodexLookup{
		{Origin: "user environment", Lookup: func(context.Context) (string, error) {
			return registryEnv(registry.CURRENT_USER, `Environment`)
		}},
		{Origin: "machine environment", Lookup: func(context.Context) (string, error) {
			return registryEnv(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`)
		}},
	}
}

// registryEnv reads CODEX_HOME under key, expanding %VARS% in a
// REG_EXPAND_SZ value. A missing key or value is "", not an error.
func registryEnv(root registry.Key, path string) (string, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer k.Close()
	v, typ, err := k.GetStringValue("CODEX_HOME")
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if typ == registry.EXPAND_SZ {
		if x, err := registry.ExpandString(v); err == nil {
			v = x
		}
	}
	return v, nil
}
