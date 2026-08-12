# Third-party notices

EverythingShare interoperates with or depends on the following projects:

| Project | Use | License |
|---|---|---|
| Everything by voidtools | External dependency in standard deployments; official portable x64 executable embedded only in the one-click demo | MIT-style license |
| OAuth2 Proxy | Optional OIDC authentication container | MIT |
| Go | Build toolchain and runtime | BSD-style |
| `golang.org/x/crypto` | Argon2id implementation | BSD-style |
| `modernc.org/sqlite` | Pure-Go SQLite driver | BSD-3-Clause |
| SQLite | Embedded database engine within the driver | Public Domain |
| Nginx | Bundled Edge container | BSD-2-Clause |
| Alpine Linux | Container base image | Multiple open-source licenses |

Container images and Go modules remain subject to their respective license notices. The demo release redistributes the unmodified official Everything 1.4.1.1032 x64 portable executable together with its license and source metadata. Release builds verify the upstream archive and executable with pinned SHA256 values before embedding them.

Official references:

- <https://www.voidtools.com/License.txt>
- <https://github.com/oauth2-proxy/oauth2-proxy>
- <https://go.dev/LICENSE>
- <https://gitlab.com/cznic/sqlite/-/blob/master/LICENSE>
- <https://nginx.org/LICENSE>
