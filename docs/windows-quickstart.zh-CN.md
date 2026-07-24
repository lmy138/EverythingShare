# EverythingShare Windows 单文件快速入门

这是无需 Docker 的 BasicAuth 本地体验版本。程序不会捆绑 Everything，你仍需自行安装 Everything。

## 准备 Everything

在 Everything 中打开“工具 → 选项 → HTTP 服务器”：

1. 启用 HTTP 服务器，建议端口使用 `8081`。
2. 设置独立的 HTTP 用户名和强密码。
3. 允许文件下载。
4. 不要把 `8081` 直接开放到公网。

## 首次运行

1. 解压整个 ZIP。
2. 双击 `EverythingShare.exe` 或 `Start EverythingShare.cmd`。
3. 按向导填写 Everything HTTP 地址、账号和密码。
4. 设置 EverythingShare 的 BasicAuth 登录账号和密码。
5. 向导完成后程序会直接启动，并自动打开浏览器。

当前社区构建暂未购买 Windows 代码签名证书。若 SmartScreen 提示“Windows 已保护你的电脑”，请先核对 Release 中的 `SHA256SUMS.txt`，确认下载来源和哈希无误后再选择“更多信息 → 仍要运行”。

默认监听 `127.0.0.1:8088`，浏览器访问：

```text
http://127.0.0.1:8088
```

首次访问会显示浏览器自带的账号密码对话框。这里填写的是向导中创建的 EverythingShare 登录账号，不是 Everything HTTP 账号。

## 生成的文件

- `everythingshare.json`：运行配置和凭据，仅当前用户、SYSTEM 和管理员应能读取。
- `data\share-gateway.db`：分享记录数据库。
- `cache\`：ZIP 缓存。

BasicAuth 密码只保存 BCrypt 哈希；连接 Everything 所需的 HTTP 密码必须保存在本地配置中。不要分享或上传 `everythingshare.json`。

## 重新配置

```powershell
.\EverythingShare.exe setup --force
```

重新配置会生成新的会话密钥。已有分享仍能验证提取码，但管理页可能无法重新显示旧提取码，因此操作前请备份配置和 `data` 目录。

## 公网部署

单文件模式默认只监听本机，适合体验和局域网前的受控入口。公网使用必须增加 HTTPS 反向代理，并谨慎修改监听地址和 `public_base_url`。需要 OIDC、独立公开分享域名或更完整的边界隔离时，请使用项目 README 中的 Docker 部署方案。
