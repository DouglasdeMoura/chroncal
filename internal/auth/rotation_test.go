package auth

import "testing"

// TestRotationCredential pins the rotation mapping for the CLI and the TUI.
// The unused source is cleared so a stale password cannot conflict with a new
// password command, and back. A basic rotation clears the whole token triple,
// because the CalDAV client reads AccessToken before the password. A bearer
// rotation clears the refresh token and the expiry, which no pasted token
// has. A command-only rotation keeps no secret, so it stays available with no
// keyring.
func TestRotationCredential(t *testing.T) {
	cases := []struct {
		name       string
		base       Credential
		secret     string
		command    string
		bearer     bool
		check      func(Credential) bool
		wantSecret bool
	}{
		{
			name:   "bearer clears a password",
			base:   Credential{Password: "old"},
			secret: "new-token", bearer: true,
			check: func(c Credential) bool {
				return c.AccessToken == "new-token" && c.Password == "" && c.PasswordCommand == ""
			},
			wantSecret: true,
		},
		{
			name:   "bearer clears a password command",
			base:   Credential{PasswordCommand: "pass show old"},
			secret: "new-token", bearer: true,
			check: func(c Credential) bool {
				return c.AccessToken == "new-token" && c.Password == "" && c.PasswordCommand == ""
			},
			wantSecret: true,
		},
		{
			name: "bearer clears a stale refresh token and expiry",
			base: Credential{
				RefreshToken: "stale-refresh",
				TokenExpiry:  "2026-01-01T00:00:00Z",
			},
			secret: "new-token", bearer: true,
			check: func(c Credential) bool {
				return c.AccessToken == "new-token" &&
					c.RefreshToken == "" && c.TokenExpiry == ""
			},
			wantSecret: true,
		},
		{
			name: "bearer keeps the OAuth client config",
			base: Credential{
				OAuthClientID:     "cid.apps.googleusercontent.com",
				OAuthClientSecret: "GOCSPX-x",
			},
			secret: "new-token", bearer: true,
			check: func(c Credential) bool {
				return c.OAuthClientID == "cid.apps.googleusercontent.com" &&
					c.OAuthClientSecret == "GOCSPX-x"
			},
			wantSecret: true,
		},
		{
			name:   "password clears a stale command",
			base:   Credential{PasswordCommand: "pass show old"},
			secret: "new-pw",
			check: func(c Credential) bool {
				return c.Password == "new-pw" && c.PasswordCommand == ""
			},
			wantSecret: true,
		},
		{
			name:    "password command clears a stale password",
			base:    Credential{Password: "old"},
			command: "pass show caldav",
			check: func(c Credential) bool {
				return c.Password == "" && c.PasswordCommand == "pass show caldav"
			},
			wantSecret: false,
		},
		{
			name: "password clears a stale token triple",
			base: Credential{
				AccessToken:  "stale-token",
				RefreshToken: "stale-refresh",
				TokenExpiry:  "2026-01-01T00:00:00Z",
			},
			secret: "new-pw",
			check: func(c Credential) bool {
				return c.Password == "new-pw" && c.AccessToken == "" &&
					c.RefreshToken == "" && c.TokenExpiry == ""
			},
			wantSecret: true,
		},
		{
			name: "password command clears a stale token triple",
			base: Credential{
				AccessToken:  "stale-token",
				RefreshToken: "stale-refresh",
				TokenExpiry:  "2026-01-01T00:00:00Z",
			},
			command: "pass show caldav",
			check: func(c Credential) bool {
				return c.PasswordCommand == "pass show caldav" && c.AccessToken == "" &&
					c.RefreshToken == "" && c.TokenExpiry == ""
			},
			wantSecret: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.base.AccountID, tc.base.Username = 7, "alice"
			if err := tc.base.ValidatePasswordSources(); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			got := RotationCredential(tc.base, tc.secret, tc.command, tc.bearer)
			if got.AccountID != 7 || got.Username != "alice" {
				t.Fatal("rotation changed the credential identity")
			}
			if !tc.check(got) {
				t.Fatalf("RotationCredential() = %+v", got)
			}
			if got.HasStoredSecret() != tc.wantSecret {
				t.Fatalf("HasStoredSecret() = %v, want %v", got.HasStoredSecret(), tc.wantSecret)
			}
		})
	}
}
