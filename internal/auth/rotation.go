package auth

// RotationCredential maps the rotation inputs onto the loaded credential. The
// CLI and the TUI share it, so one rotation contract applies to both.
//
// The rotation clears the source the user does not use. A stale password
// cannot then conflict with a new password command, and back. A basic
// rotation clears the whole token triple, and a bearer rotation clears both
// password sources. The CalDAV client reads AccessToken before the password,
// so a token left on the credential would win over a new password.
//
// A bearer rotation clears the refresh token and the token expiry too. The
// user pastes the token, so nothing refreshes it and no expiry applies. A
// value left from an earlier OAuth credential would claim otherwise.
//
// The OAuth client ID and the client secret stay. They are connection config,
// not the secret this function rotates, and "account reauth" reuses them.
//
// The caller passes at most one of secret and secretCommand. Bearer auth
// carries the token in secret.
func RotationCredential(base Credential, secret, secretCommand string, bearer bool) Credential {
	cred := base
	if bearer {
		cred.AccessToken = secret
		cred.RefreshToken = ""
		cred.TokenExpiry = ""
		cred.Password = ""
		cred.PasswordCommand = ""
		return cred
	}
	cred.AccessToken = ""
	cred.RefreshToken = ""
	cred.TokenExpiry = ""
	if secretCommand != "" {
		cred.Password = ""
		cred.PasswordCommand = secretCommand
		return cred
	}
	cred.Password = secret
	cred.PasswordCommand = ""
	return cred
}
