//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
)

// writeSecrets stages the file next to path, restricts the staged file to the
// current user, then renames it into place. The staged file inherits the
// directory ACL (under %LOCALAPPDATA% that can include sandbox groups, under a
// custom --home Authenticated Users), so the DACL is applied before any
// reader can open secrets.json rather than after; an NTFS rename keeps the
// file's own security descriptor.
func writeSecrets(path string, data []byte) error {
	staged := path + ".new"
	if err := fsx.WriteFileAtomic(staged, data, 0o600); err != nil {
		return err
	}
	if err := restrictToCurrentUser(staged); err != nil {
		os.Remove(staged)
		return err
	}
	if err := fsx.Rename(staged, path); err != nil {
		os.Remove(staged)
		return err
	}
	return nil
}

// ensureSecretsACL re-applies the user-only DACL when secrets.json is readable
// by any other principal (a file from an older build, or a hand-copied one).
func ensureSecretsACL(path string) error {
	ok, err := userOnlyACL(path)
	if err != nil || ok {
		return err
	}
	return restrictToCurrentUser(path)
}

// restrictToCurrentUser replaces the DACL with a protected one (no inherited
// ACEs) that grants full control to the current user's SID and nothing to
// anyone else. Working with the SID rather than the account name keeps this
// independent of the username (spaces, non-ASCII, domain prefixes).
func restrictToCurrentUser(path string) error {
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;FA;;;%s)", me))
	if err != nil {
		return fmt.Errorf("build user-only DACL: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("build user-only DACL: %w", err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("restrict %s to current user: %w", path, err)
	}
	return nil
}

// aclEntry is one ACE of a file DACL as read back from the file system.
type aclEntry struct {
	SID       *windows.SID
	Allow     bool
	Inherited bool
}

// readDACL returns whether the DACL is protected (inheritance off) and its
// entries. A NULL DACL, which grants everyone everything, comes back as
// protected=false with no entries.
func readDACL(path string) (protected bool, entries []aclEntry, err error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, nil, fmt.Errorf("read DACL of %s: %w", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		return false, nil, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
			return false, nil, nil
		}
		return false, nil, err
	}
	if dacl == nil {
		return false, nil, nil
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, nil, err
		}
		entries = append(entries, aclEntry{
			SID:       (*windows.SID)(unsafe.Pointer(&ace.SidStart)),
			Allow:     ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE,
			Inherited: ace.Header.AceFlags&windows.INHERITED_ACE != 0,
		})
	}
	return control&windows.SE_DACL_PROTECTED != 0, entries, nil
}

// userOnlyACL reports whether path has a protected DACL whose only entries
// are allow ACEs for the current user.
func userOnlyACL(path string) (bool, error) {
	me, err := currentUserSID()
	if err != nil {
		return false, err
	}
	protected, entries, err := readDACL(path)
	if err != nil || !protected || len(entries) == 0 {
		return false, err
	}
	for _, e := range entries {
		if !e.Allow || e.Inherited || !e.SID.Equals(me) {
			return false, nil
		}
	}
	return true, nil
}

func currentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("current user SID: %w", err)
	}
	return u.User.Sid, nil
}
