//go:build windows

package credentials

import (
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"os/user"
	"unsafe"
)

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrVault
	}
	current, err := user.Current()
	if err != nil {
		return ErrVault
	}
	// Explicit SID grants avoid localized account names. No command shell or secret arguments.
	return exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", "*"+current.Uid+":(OI)(CI)F", "*S-1-5-18:(OI)(CI)F").Run()
}
func dpapi(data []byte, decrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, ErrVault
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	var err error
	if decrypt {
		err = windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	if err != nil {
		return nil, ErrVault
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
func protect(data []byte) ([]byte, error)   { return dpapi(data, false) }
func unprotect(data []byte) ([]byte, error) { return dpapi(data, true) }
