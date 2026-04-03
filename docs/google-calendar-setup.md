# Google Calendar Setup

chroncal syncs with Google Calendar via CalDAV. Google requires OAuth 2.0
authentication with your own client credentials.

## Step 1: Create OAuth Client ID

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project (or select an existing one)
3. Enable the **Google Calendar API**:
   - Go to **APIs & Services > Library**
   - Search for "Google Calendar API"
   - Click **Enable**
4. Create OAuth credentials:
   - Go to **APIs & Services > Credentials**
   - Click **Create Credentials > OAuth client ID**
   - Application type: **Desktop app**
   - Name: `chroncal` (or anything you like)
   - Click **Create**
5. Copy the **Client ID** and **Client Secret**

## Step 2: Add Account

```bash
chroncal account add "Google Work" \
    --server https://apidata.googleusercontent.com/caldav/v2 \
    --auth oauth2 \
    --oauth-client-id "YOUR_CLIENT_ID.apps.googleusercontent.com" \
    --oauth-client-secret "YOUR_SECRET" \
    --allow-plaintext
```

A browser window will open for Google authorization. Grant calendar access
when prompted. If no browser is available (SSH, headless), the URL will be
printed for manual authorization.

## Step 3: Discover Calendars

```bash
chroncal account discover "Google Work"
```

This lists all calendars on your Google account. Link them to local calendars
or create new ones.

## Step 4: Sync

```bash
chroncal sync run
```

## Limitations

- **Google CalDAV only supports VEVENT.** VTODO and VJOURNAL sync is not
  available with Google Calendar. Use Nextcloud, Radicale, or Fastmail for
  full component support.
- Google does not support `sync-collection` REPORT. chroncal automatically
  falls back to ctag + ETag comparison for change detection.

## Credential Storage

By default, chroncal stores credentials in the OS keyring (GNOME Keyring,
KWallet, macOS Keychain). If no keyring is available, use `--allow-plaintext`
to store credentials in `~/.config/chroncal/credentials/` with `0600`
permissions.

## Troubleshooting

- **"redirect_uri_mismatch"**: Ensure your OAuth client type is "Desktop app",
  not "Web application".
- **"access_denied"**: You may need to add your Google account as a test user
  in the OAuth consent screen if the app is in "Testing" mode.
- **Sync errors**: Run `chroncal sync reset --calendar "Calendar Name"` to
  clear sync state and force a full re-sync.
