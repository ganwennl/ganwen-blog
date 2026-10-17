# systemd 部署备选说明

当前线上已使用宝塔原生「Go项目」，见 [BT-GO-PROJECT.md](BT-GO-PROJECT.md)。以下保留为独立 systemd 部署的备选方案，不能在当前服务器重复启动另一套服务或覆盖原生项目配置。

适用于 Linux x86_64、宝塔 Nginx 1.30.4。下列命令属于新安装流程，执行前确认目标路径、端口和域名归属。

上传包：`dist/ganwen-blog-bt-20261007.tar.gz`。解压后为 `/www/wwwroot/ganwen-go/`，现有 `/www/wwwroot/ganwen.nl/` 保留。无需安装 Go、Node.js 或 PHP；默认 SQLite，无需新建数据库。

## 1. 保留回滚快照

切换期间停止在 WordPress 编辑及上传。在宝塔分别备份当前网站和 WordPress 数据库，确认备份完成。旧站目录和数据库都继续保留。

在宝塔终端以 root 保存配置和文件快照；记录输出的备份目录：

```bash
set -eu
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
BACKUP_DIR=/www/backup/ganwen-migration-$STAMP
install -d -m 700 "$BACKUP_DIR"
cp -p /www/server/panel/vhost/nginx/ganwen.nl.conf "$BACKUP_DIR/ganwen.nl.conf"
cp -p /www/server/panel/vhost/rewrite/ganwen.nl.conf "$BACKUP_DIR/wordpress-rewrite.conf"
tar -czpf "$BACKUP_DIR/wordpress-files.tar.gz" -C /www/wwwroot ganwen.nl
printf '%s\n' "$BACKUP_DIR"
```

原证据页、原 WordPress 图片和 SSL/ACME 文件保留原处作恢复资料。新版支持将 `/bewijs.html` 和全部证据导入博客独立页面；导入及 Nginx 切换必须作为同一次授权上线执行。备份和上传包包含私密内容及凭据，保存在非公开目录。

## 2. 上传并解压

在宝塔“文件”中新建 `/www/backup/ganwen-upload/`，将上传包放入该目录；不要放进原网站目录。可在本机 PowerShell 用 `Get-FileHash -Algorithm SHA256 'D:\newcode\ganwenblog\dist\ganwen-blog-bt-20261007.tar.gz'`，与服务器 `sha256sum` 输出核对。

```bash
ARCHIVE=/www/backup/ganwen-upload/ganwen-blog-bt-20261007.tar.gz
sha256sum "$ARCHIVE"
# 新安装：目标必须不存在，防止覆盖已有数据。
test ! -e /www/wwwroot/ganwen-go
tar -xzf "$ARCHIVE" --no-same-owner -C /www/wwwroot
test -f /www/wwwroot/ganwen-go/ganwen
test -f /www/wwwroot/ganwen-go/data/blog.db
```

包内目录：

```text
ganwen-go/
  ganwen                         Linux amd64 独立二进制
  data/
    blog.db                      中文原文及已导入的英/荷/西译文
    media/                       按原 URL 层级保存的媒体及兼容原图路径
    runtime.env                  管理员密码哈希/数据库运行配置
    admin-access.txt             已生成的随机管理员账号密码
    translation.key              加密翻译配置的解密密钥
    wordpress-original.json      原始内容和历史修订归档
    media-manifest.json          原媒体校验清单
    media-aliases.json           缺失原图的兼容说明
  deploy/
    ganwen-blog.service
    ganwen.nl.bt.conf.example
    BT-DEPLOY.md
  README.md                      项目说明
```

## 3. 权限和运行配置

```bash
chown root:root /www/wwwroot/ganwen-go /www/wwwroot/ganwen-go/ganwen
chmod 755 /www/wwwroot/ganwen-go /www/wwwroot/ganwen-go/ganwen
chown -R www:www /www/wwwroot/ganwen-go/data
find /www/wwwroot/ganwen-go/data -type d -exec chmod 700 {} +
find /www/wwwroot/ganwen-go/data -type f -exec chmod 600 {} +
```

保留包内 `data/runtime.env` 的管理员配置；不要运行 `init-admin` 覆盖。默认 SQLite，可在此文件保留或加入：

```dotenv
GBLOG_DATABASE_DRIVER=sqlite
GBLOG_DATABASE_PATH=blog.db
```

程序自行读取 `runtime.env`，服务无需 `EnvironmentFile`。生产索引开关是 service 的 **`--preview=false` 参数**，不是环境变量；监听必须为 `127.0.0.1:8097`，无需开放 8097 公网端口。保留 `translation.key` 并单独私密备份。

若以后使用 MySQL，另建博客专用空库和账号，在私密 `runtime.env` 配置 `GBLOG_DATABASE_DRIVER=mysql`、`GBLOG_DATABASE_DSN`，再通过 `database-export/database-import` 迁移；不能直接连接原 WordPress 库。本次上线可直接用包内 SQLite。

## 4. 启动 Go，先核对本机响应

```bash
install -m 644 /www/wwwroot/ganwen-go/deploy/ganwen-blog.service /etc/systemd/system/ganwen-blog.service
systemctl daemon-reload
systemctl enable --now ganwen-blog
systemctl status --no-pager ganwen-blog
curl -fsSI -H 'Host: www.ganwen.nl' 'http://127.0.0.1:8097/?p=1447'
curl -fsS -H 'Host: www.ganwen.nl' 'http://127.0.0.1:8097/robots.txt'
```

文章应为 200，robots 应允许公开抓取并列出 `https://www.ganwen.nl/?sitemap=index`；不能出现 `Disallow: /`。若启动失败，查看 `journalctl -u ganwen-blog -n 80 --no-pager`，处理后再切换 Nginx。

## 5. 替换该站 Nginx 配置并切换

`ganwen.nl.bt.conf.example` 是完整站点配置，替换 `/www/server/panel/vhost/nginx/ganwen.nl.conf`，不要追加到旧配置末尾。也可在宝塔“网站 → ganwen.nl → 设置 → 配置文件”粘贴完整内容。

配置保留实际域名、旧 root、现有 SSL、HTTP/3、ACME 和日志；移除 PHP 引用、WordPress rewrite 和自定义 404 跳转。不要另外开启宝塔 PHP/伪静态规则覆盖它。

```bash
cp /www/wwwroot/ganwen-go/deploy/ganwen.nl.bt.conf.example /www/server/panel/vhost/nginx/ganwen.nl.conf
/www/server/nginx/sbin/nginx -t
/www/server/nginx/sbin/nginx -s reload
```

**只有 `nginx -t` 成功后才执行 reload**；测试失败时恢复第 1 步备份配置。此前正常运行的旧 Nginx 进程仍使用原配置。

媒体顺序为 Go `data/media` → 实际 404 时 `@legacy_media` 回退旧目录：后台新上传、已迁入的证据和缺失原图的兼容文件能访问。独立页面（包括导入后的 `/bewijs.html`）统一交给 Go，不再保留该地址的旧静态 location。`/assets/` 交给 Go 提供内嵌样式脚本；整个私密 `data/` 不映射到公网。切换前须验证独立页已导入、媒体包完整、匿名直达和博客隐藏展示；未导入时不得用新配置替换现有证据页规则。

`/index.php` 使用 `proxy_pass http://127.0.0.1:8097;`，末尾不加 `/`，保留原路径和查询参数；其余旧 PHP 入口返回 404。此行为符合 [Nginx proxy_pass 文档](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass)，静态文件顺序参见 [try_files 文档](https://nginx.org/en/docs/http/ngx_http_core_module.html#try_files)。

## 6. 公网验收和后台

```bash
curl -sSI 'http://ganwen.nl/?p=1447'
curl -sSI 'https://ganwen.nl/?p=1447'
curl -fsSI 'https://www.ganwen.nl/?p=1447'
curl -fsSI 'https://www.ganwen.nl/index.php?p=1447'
curl -fsSI 'https://www.ganwen.nl/en/?p=1447'
curl -fsSI 'https://www.ganwen.nl/nl/?p=1447'
curl -fsSI 'https://www.ganwen.nl/es/?p=1447'
curl -fsSI 'https://www.ganwen.nl/bewijs.html'
curl -fsSI 'https://www.ganwen.nl/pcs.mp4'
curl -fsSI 'https://www.ganwen.nl/wp-content/uploads/2012/08/WP_000221.jpg'
curl -fsSI 'https://www.ganwen.nl/assets/public.css'
curl -fsS 'https://www.ganwen.nl/robots.txt'
curl -fsS 'https://www.ganwen.nl/?sitemap=index'
curl -fsS 'https://www.ganwen.nl/?feed=rss2' > /dev/null
curl -sSI 'https://www.ganwen.nl/this-page-does-not-exist'
```

HTTP 转 HTTPS、裸域博客转 www，均保留原查询参数。已发布四语页面应为 200，未知地址必须是真 404。打开文章源代码，核对中文 canonical 为原 `https://www.ganwen.nl/?p=1447`，译文 self-canonical、四语 hreflang，公开内容没有 `noindex`；分类、标签、分页与页面 `/?page_id=2` 继续使用旧格式。

`WP_000221.jpg` 为缺失原图路径的 224×300 缩略图兼容副本，并非恢复了原始分辨率；此 URL 的 200 可验证 `@go_media` 回退实际生效。

打开 `https://www.ganwen.nl/admin/`，从服务器私密 `data/admin-access.txt` 读取随机账号密码登录。核对全部文章和媒体、四语切换、手机显示，再上传一张图片验证返回的 `/wp-content/uploads/...` 可公网访问。后台“自动翻译”配置自己的 HTTPS API 地址、模型、Key；未配置时不会调用付费接口。

## 7. 回滚

把下面备份目录替换为第 1 步实际记录值，先恢复旧路由，再停 Go：

```bash
BACKUP_DIR=/www/backup/ganwen-migration-替换为实际时间
cp -p "$BACKUP_DIR/ganwen.nl.conf" /www/server/panel/vhost/nginx/ganwen.nl.conf
/www/server/nginx/sbin/nginx -t
/www/server/nginx/sbin/nginx -s reload
systemctl disable --now ganwen-blog
```

旧站文件和数据库未修改，恢复原配置后 WordPress 可继续使用；原证据页及媒体同样保留。若上线后已在 Go 新增文章或上传，先备份 Go 数据和媒体，再决定如何同步这些新内容；不删除 `ganwen-go/data`。后续 SQLite 备份优先用逻辑导出，或停 Go 后复制完整 data，避免只复制正在写入的 `blog.db` 而遗漏 WAL。
