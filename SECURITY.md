# Security Policy

## Report a vulnerability

If you find a security vulnerability in chroncal, report it in private.

**Do not open a public issue.**

Use [GitHub's private vulnerability reporting](https://github.com/douglasdemoura/chroncal/security/advisories/new) to send your report. You can also email [security@douglasmoura.dev](mailto:security@douglasmoura.dev).

Include:

- A description of the vulnerability
- Steps that reproduce the issue
- Versions that have the issue
- The possible impact

You should receive an acknowledgment within 48 hours. We will work with you to understand the issue. We will coordinate a fix before any public disclosure.

## Supported versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |

## Scope

chroncal stores data in a local SQLite database. The main security areas are:

- **iCal import** -- The import path parses untrusted `.ics` files. chroncal enforces payload and inline attachment size limits on import.
- **Account credentials** -- The OS keyring stores credentials by default. Plaintext storage is opt-in only for environments without a usable keyring.
- **OAuth tokens** -- Google CalDAV uses PKCE. The configured credential store keeps access tokens after a refresh.
- **SMTP credentials** -- Config files store SMTP credentials. The `smtp.password_cmd` option runs a command at send time and keeps the secret out of the config file.
- **Remote CalDAV servers** -- Sync, discovery, and free/busy requests use bounded HTTP clients and command deadlines.
- **Desktop notifications** -- D-Bus on Linux.
