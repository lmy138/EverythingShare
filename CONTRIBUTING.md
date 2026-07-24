# Contributing

Contributions are welcome.

## Development workflow

1. Create a focused branch.
2. Keep personal domains, paths, credentials and generated `.env` files out of commits.
3. Add or update tests for behavior changes.
4. Run:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-alpine3.23 sh -c "go test ./... && go vet ./..."
docker build -t everythingshare:test .
```

5. Explain security and compatibility effects in the pull request.

## Scope

Good contributions include:

- Everything HTTP compatibility
- safer sharing controls
- mobile and accessibility improvements
- Basic/OIDC deployment templates
- documentation and reproducible tests

Avoid adding:

- bundled Everything binaries
- provider-specific secrets
- telemetry enabled by default
- hard-coded personal infrastructure

## UI changes

Preserve:

- keyboard access
- 40-pixel mobile touch targets
- compact high-density result lists
- Chinese file names and long paths
- desktop column resizing
