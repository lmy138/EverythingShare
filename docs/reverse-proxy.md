# HTTPS 反向代理

EverythingShare 的内置 Edge 默认只监听 `127.0.0.1:8088`，外层反向代理负责证书和公网入口。

两个域名必须同时转发到同一个 Edge 端口，并保留原始 `Host`。

## Nginx 示例

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name everything.example.com;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/private.key;

    location / {
        proxy_pass http://127.0.0.1:8088;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}

server {
    listen 443 ssl;
    server_name share.example.com;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/private.key;

    location / {
        proxy_pass http://127.0.0.1:8088;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

可额外在 80 端口增加 HTTPS 跳转。

## nginx-ui / Nginx Proxy Manager

为两个域名分别创建站点：

| 域名 | 上游 |
|---|---|
| `everything.example.com` | `http://127.0.0.1:8088` |
| `share.example.com` | `http://127.0.0.1:8088` |

开启：

- WebSocket/HTTP 1.1
- 保留 Host
- 长超时
- 关闭代理缓冲
- 强制 HTTPS

不要把 Gateway 容器的 `8080` 或 Everything 的 `8081` 暴露到公网。

## 使用非标准 HTTPS 端口

如果地址为 `https://share.example.com:8443`，必须把端口写入：

```dotenv
PUBLIC_BASE_URL=https://share.example.com:8443
OIDC_REDIRECT_URL=https://everything.example.com:8443/oauth2/callback
```

浏览器访问域名、OIDC 客户端回调和 `.env` 中的端口必须完全一致。

## 验收

1. 未登录访问搜索域名应要求 Basic 或 OIDC 登录。
2. 访问分享域名根路径可到达 Gateway。
3. 直接访问 `8081` 应被 Windows 防火墙拦截。
4. 大文件下载返回 `Accept-Ranges`，断点续传得到 HTTP 206。
5. `http://` 请求应跳转 HTTPS。
