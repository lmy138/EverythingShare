# OIDC 登录

OIDC 模式保护 Everything 搜索页和分享管理页。公开分享域名仍由提取码保护，不会跳转身份服务。

## 1. 创建 OIDC Web 应用

在身份服务中创建 Traditional Web / Web Application：

```text
Redirect URI:
https://everything.example.com/oauth2/callback
```

建议 Scope：

```text
openid profile email
```

只把可信用户或用户组分配给该应用。应用内部不会根据电子邮箱自动授予额外权限，OIDC Provider 的应用访问规则是第一道授权边界。

## 2. 配置 `.env`

```dotenv
EVERYTHING_HOST=everything.example.com
SHARE_HOST=share.example.com
PUBLIC_BASE_URL=https://share.example.com
COOKIE_SECURE=true

OIDC_ISSUER_URL=https://identity.example.com/oidc
OIDC_CLIENT_ID=your-client-id
OIDC_CLIENT_SECRET=your-client-secret
OIDC_COOKIE_SECRET=32-byte-base64url-secret
OIDC_REDIRECT_URL=https://everything.example.com/oauth2/callback
```

推荐使用引导脚本生成 Cookie secret：

```powershell
.\scripts\setup.ps1 -AuthMode Oidc
```

## 3. 启动

```powershell
docker compose -f docker-compose.yml -f docker-compose.oidc.yml up -d --build
```

## 4. Provider 示例

### Logto

Issuer 通常为：

```text
https://your-logto-domain/oidc
```

在 Logto 中关闭公开注册，并使用应用级访问控制限制用户。

### Authentik

使用 Provider 页面给出的 OpenID Configuration Issuer，通常类似：

```text
https://auth.example.com/application/o/everythingshare/
```

### Keycloak

Issuer 通常为：

```text
https://auth.example.com/realms/your-realm
```

## 常见问题

### `redirect_uri` 不匹配

浏览器地址、`.env` 的 `OIDC_REDIRECT_URL` 和 Provider 注册值必须逐字符一致，包括：

- `https`
- 端口
- 路径
- 域名大小写规则

### 登录后 403

确认：

- 用户已分配给 OIDC 应用
- Provider 返回 `email` 或可识别的用户声明
- `oauth2-proxy` 日志没有未验证邮箱错误

查看日志：

```powershell
docker compose -f docker-compose.yml -f docker-compose.oidc.yml logs --tail 100 oauth2-proxy edge
```

### 从 Basic 切换 OIDC

不要重新生成或删除 `SESSION_SECRET`。只补充 OIDC 配置，然后使用两份 Compose 文件启动即可。这样已有分享的一键提取码仍可在管理页显示。
