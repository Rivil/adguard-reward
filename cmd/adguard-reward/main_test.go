package main

import (
	"testing"
)

func TestEnvOr(t *testing.T) {
	t.Setenv("ADGUARD_REWARD_TEST_KEY", "set")
	if got := envOr("ADGUARD_REWARD_TEST_KEY", "fallback"); got != "set" {
		t.Fatalf("envOr with set var = %q, want %q", got, "set")
	}
	if got := envOr("ADGUARD_REWARD_TEST_MISSING", "fallback"); got != "fallback" {
		t.Fatalf("envOr with missing var = %q, want %q", got, "fallback")
	}
}
