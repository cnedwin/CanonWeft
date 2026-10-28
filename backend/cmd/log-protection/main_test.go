package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSetupRejectsArgumentsWithoutExposingConnection(t *testing.T) {
	t.Setenv("STUDIO_LOG_ADMIN_DSN", "private-password malformed connection")
	for _, args := range [][]string{{"-unknown", "private-password"}, {"unexpected"}, {"-source", "studio_test"}} {
		var output bytes.Buffer
		err := run(context.Background(), args, &output)
		if err == nil || strings.Contains(err.Error(), "private-password") || output.Len() != 0 {
			t.Fatal("invalid setup exposed connection or succeeded")
		}
	}
}
