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

func TestReadBasicSecretUsesPasswordCmdFlag(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "")
	t.Setenv("CHRONCAL_PASSWORD_CMD", "env-cmd")
	password, cmd, err := readBasicSecret("pass show caldav_password")
	if err != nil {
		t.Fatalf("readBasicSecret: %v", err)
	}
	if password != "" || cmd != "pass show caldav_password" {
		t.Fatalf("password=%q cmd=%q, want empty password and flag command", password, cmd)
	}
}

func TestReadBasicSecretUsesEnvCmd(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "")
	t.Setenv("CHRONCAL_PASSWORD_CMD", "secret-tool lookup caldav host")
	password, cmd, err := readBasicSecret("")
	if err != nil {
		t.Fatalf("readBasicSecret: %v", err)
	}
	if password != "" || cmd != "secret-tool lookup caldav host" {
		t.Fatalf("password=%q cmd=%q, want env command", password, cmd)
	}
}

func TestReadBasicSecretRejectsPasswordAndCmd(t *testing.T) {
	t.Setenv("CHRONCAL_PASSWORD", "literal")
	t.Setenv("CHRONCAL_PASSWORD_CMD", "echo from-cmd")
	if _, _, err := readBasicSecret(""); err == nil {
		t.Fatal("expected error when password and password_cmd are both set")
	}
}
