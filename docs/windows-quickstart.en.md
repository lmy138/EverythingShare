# EverythingShare single-file Windows quick start

This package provides a Docker-free local Basic Auth experience. Everything itself is not bundled and must be installed separately.

## Prepare Everything

Open **Tools → Options → HTTP Server** in Everything:

1. Enable the HTTP server, preferably on port `8081`.
2. Set a dedicated HTTP username and a strong password.
3. Allow file downloads.
4. Do not expose port `8081` directly to the internet.

## First run

1. Extract the complete ZIP.
2. Double-click `EverythingShare.exe` or `Start EverythingShare.cmd`.
3. Enter the Everything HTTP address and credentials in the wizard.
4. Create the Basic Auth username and password for EverythingShare.
5. The program starts and opens the browser automatically when setup finishes.

Community builds are not currently Authenticode-signed. If SmartScreen displays “Windows protected your PC,” first verify the download against `SHA256SUMS.txt` in the Release. Continue through **More info → Run anyway** only when the source and hash match.

The default listener is `127.0.0.1:8088`:

```text
http://127.0.0.1:8088
```

The browser displays its built-in credential dialog. Enter the EverythingShare account created by the wizard, not the Everything HTTP account.

## Generated files

- `everythingshare.json`: local configuration and credentials; access should be limited to the current user, SYSTEM, and administrators.
- `data\share-gateway.db`: the sharing database.
- `cache\`: ZIP cache.

The Basic Auth password is stored only as a BCrypt hash. The Everything HTTP password must remain in the local configuration so the gateway can connect upstream. Never share or upload `everythingshare.json`.

## Run setup again

```powershell
.\EverythingShare.exe setup --force
```

Setup generates new session keys. Existing extraction codes remain verifiable, but the management page might no longer reproduce old codes. Back up the configuration and `data` directory first.

## Internet-facing deployment

Single-file mode binds only to the local computer by default and is intended for evaluation or a controlled LAN edge. Internet exposure requires an HTTPS reverse proxy and deliberate changes to the listener and `public_base_url`. Use the Docker deployment documented in the main README when you need OIDC, a separate public sharing host, or stronger edge isolation.
