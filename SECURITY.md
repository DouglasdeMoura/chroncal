# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in chroncal, please report it responsibly.

**Do not open a public issue.**

Instead, use [GitHub's private vulnerability reporting](https://github.com/douglasdemoura/chroncal/security/advisories/new) to submit your report. You can also email [security@douglasmoura.dev](mailto:security@douglasmoura.dev).

Please include:

- A description of the vulnerability
- Steps to reproduce
- Affected versions
- Any potential impact

You should receive an acknowledgment within 48 hours. We will work with you to understand the issue and coordinate a fix before any public disclosure.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |

## Scope

chroncal stores data locally in a SQLite database. The main areas of security concern are:

- **iCal import** -- parsing untrusted `.ics` files; chroncal enforces payload and inline attachment size limits during import
- **Account credentials** -- stored in the OS keyring by default; plaintext storage is opt-in only for environments without a usable keyring
- **OAuth tokens** -- Google CalDAV uses PKCE and persists refreshed access tokens through the configured credential store
- **SMTP credentials** -- stored in config files
- **Remote CalDAV servers** -- sync, discovery, and free/busy requests run with bounded HTTP clients and command deadlines
- **Desktop notifications** -- D-Bus interaction on Linux
