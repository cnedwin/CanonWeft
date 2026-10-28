//go:build windows

package readercredentials

import (
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"os/user"
	"unsafe"
)

func privateFile(_ *os.File, path string) error {
	current, err := user.Current()
	if err != nil {
		return ErrProtectedFile
	}
	return exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", "*"+current.Uid+":F", "*S-1-5-18:F").Run()
}

func protect(raw []byte, decrypt bool) ([]byte, error) {
	if len(raw) == 0 {
		return nil, ErrProtectedFile
	}
	input := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var output windows.DataBlob
	var err error
	if decrypt {
		err = windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	if err != nil {
		return nil, ErrProtectedFile
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
