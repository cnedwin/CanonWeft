package main

import (
	"testing"
)

func TestAuthorityProvisionRejectsUnacknowledgedOrUnsafeTargets(t *testing.T) {
	for _, args := range [][]string{
		{"-source", "studio_test", "-app-user", "studio_test"},
		{"-services-stopped", "-source", "studio_test`; DROP DATABASE studio", "-app-user", "studio_test"},
		{"-services-stopped", "-source", "studio_test", "-app-user", "root"},
		{"-services-stopped", "-source", "studio_test", "-app-user", "studio'@'%'"},
		{"-services-stopped", "-source", "studio_test", "-app-user", "studio_test", "-schema", "studio_test"},
	} {
		if err := validateArgs(args); err == nil {
			t.Fatalf("unsafe provisioning accepted: %v", args)
		}
	}
}
