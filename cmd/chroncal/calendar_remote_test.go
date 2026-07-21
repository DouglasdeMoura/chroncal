package main

import (
	"testing"
)

func TestReadBasicPasswordUsesEnvVar(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "lab-secret")
	got, err := readBasicPassword()
	if err != nil {
		t.Fatalf("readBasicPassword: %v", err)
	}
	if got != "lab-secret" {
		t.Fatalf("password = %q, want lab-secret", got)
	}
}

func TestReadBasicPasswordRequiresEnvWhenNonInteractive(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "")
	if _, err := readBasicPassword(); err == nil {
		t.Fatal("expected error when stdin is not a terminal and env is unset")
	}
}
