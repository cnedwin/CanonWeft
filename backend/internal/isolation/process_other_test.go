//go:build !windows

package isolation

import "testing"

func assertNoEmulators(t *testing.T, root string) { t.Fatal("Windows runtime required") }
