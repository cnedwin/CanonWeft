//go:build !windows

package credentials

// Other hosts must supply a qualified operating-system secret store before use.
func privateDirectory(string) error    { return ErrVault }
func protect([]byte) ([]byte, error)   { return nil, ErrVault }
func unprotect([]byte) ([]byte, error) { return nil, ErrVault }
