//go:build !windows

package readercredentials

import "os"

func privateFile(*os.File, string) error   { return ErrProtectedFile }
func protect([]byte, bool) ([]byte, error) { return nil, ErrProtectedFile }
