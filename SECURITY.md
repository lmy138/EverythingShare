# Security Policy

## Supported version

The latest tagged release receives security fixes. Development snapshots are provided without a security support guarantee.

## Reporting a vulnerability

Please use the repository's private GitHub Security Advisory feature:

1. Open the repository's **Security** tab.
2. Choose **Advisories**.
3. Select **Report a vulnerability**.

Do not publish extraction-code bypasses, authentication bypasses, secret leaks, path traversal findings or denial-of-service details in a public issue before a fix is available.

Include:

- affected version or commit
- deployment mode: Basic or OIDC
- minimal reproduction steps
- expected and observed behavior
- impact

## Deployment security requirements

- Keep Everything HTTP port and the Gateway container off the public network.
- Bind the bundled Edge to `127.0.0.1` when using an external TLS proxy.
- Use HTTPS and `COOKIE_SECURE=true` in production.
- Protect `.env`, `secrets/htpasswd`, database backups and Docker access.
- Use a separate strong password for Everything HTTP Server.
- Do not reuse OIDC client secrets between services.
- Back up `SESSION_SECRET`; do not rotate it without understanding the effect on encrypted extraction-code copies.
- Restrict access to Docker Desktop and the Windows account running the service.

## Threat-model notes

- Download tickets are bearer URLs and remain usable until their short expiry, share expiry or revocation.
- Rate limiting trusts `X-Forwarded-For` only when `TRUST_PROXY_HEADERS=true`. This is safe with the bundled private Edge; do not expose Gateway directly in that mode.
- Public sharing can consume disk, CPU and upstream bandwidth. Set expiration and download limits for untrusted recipients.
