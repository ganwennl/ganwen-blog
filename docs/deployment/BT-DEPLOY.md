# systemd + Nginx 部署说明

除宝塔原生 Go 项目（见 [BT-GO-PROJECT.md](BT-GO-PROJECT.md)）外，也可以使用 Linux `systemd` 管理后台服务，配合 Nginx 反向代理对外提供 HTTPS 访问。同一台服务器只应选择其中一种托管方式。

## 1. 目录与权限

将 Linux amd64 程序部署到 `/www/wwwroot/ganwen-go/`：

```text
ganwen-go/
  ganwen                         Linux amd64 可执行文件
  data/
    blog.db                      SQLite 数据库
    media/                       上传媒体目录
    runtime.env                  管理员哈希与数据库配置
    admin-access.txt             首次生成的管理员初始密码
    translation.key              翻译服务配置加密密钥（启用翻译时生成）
  deploy/
    ganwen-blog.service
    ganwen.nl.bt.conf.example
```

设置目录与文件权限（假设 Web/运行用户为 `www`）：

```bash
chown root:root /www/wwwroot/ganwen-go /www/wwwroot/ganwen-go/ganwen
chmod 755 /www/wwwroot/ganwen-go /www/wwwroot/ganwen-go/ganwen
chown -R www:www /www/wwwroot/ganwen-go/data
find /www/wwwroot/ganwen-go/data -type d -exec chmod 700 {} +
find /www/wwwroot/ganwen-go/data -type f -exec chmod 600 {} +
```

默认使用 SQLite；如需改用独立 MySQL 空库，可在 `data/runtime.env` 中配置 `GBLOG_DATABASE_DRIVER=mysql` 与 `GBLOG_DATABASE_DSN`，并通过 `database-export` / `database-import` 迁移数据。

## 2. 配置 systemd 服务

参考 [ganwen-blog.service](../../deploy/ganwen-blog.service) 安装单元文件并启动：

```bash
install -m 644 /www/wwwroot/ganwen-go/deploy/ganwen-blog.service /etc/systemd/system/ganwen-blog.service
systemctl daemon-reload
systemctl enable --now ganwen-blog
systemctl status --no-pager ganwen-blog
```

生产环境应保持 `--preview=false` 并只监听本机回环地址（如 `127.0.0.1:8097`），无需向公网开放该端口。先在本机测试响应：

```bash
curl -fsSI -H 'Host: www.example.com' 'http://127.0.0.1:8097/'
curl -fsS -H 'Host: www.example.com' 'http://127.0.0.1:8097/robots.txt'
```

## 3. 配置 Nginx 反向代理

参考 [ganwen.nl.bt.conf.example](../../deploy/ganwen.nl.bt.conf.example)，将域名、证书路径与反代端口调整为实际环境后测试并重载：

```bash
nginx -t && nginx -s reload
```

注意事项：
- `/index.php` 的 `proxy_pass` 末尾不加 `/`，以保留旧链接查询参数（如 `/index.php?p=ID`）。
- `/assets/` 与 `/admin/` 转发给 Go 服务，私密的 `data/` 目录不要直接暴露为静态目录。

## 4. 备份与回滚

升级或迁移前，先备份 Nginx 配置及完整的 `data/` 目录（停止服务后复制，或执行 `ganwen database-export` 导出逻辑备份并单独保存 `.translation.key`）。需要回退时，先恢复旧可执行文件或旧站点配置，确认 `nginx -t` 通过后再重载服务。
