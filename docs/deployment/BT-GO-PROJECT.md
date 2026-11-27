# 宝塔 Go 项目部署说明

本文档说明如何通过宝塔面板「网站 → Go项目」部署与日常更新 `ganwen-blog`。编译好的 Linux amd64 二进制可直接运行，服务器无需安装 Go 工具链。

## 参考配置

| 项目 | 示例配置 |
| --- | --- |
| 项目名称 | `ganwen_blog` |
| 执行文件 | `/www/wwwroot/ganwen-go/ganwen` |
| 监听端口 | `127.0.0.1:8099`（仅本机回环，不放行公网端口） |
| 运行用户 | `www` |
| 开机启动 | 开启（由面板守护进程管理） |
| 运行环境 | 无需填写，程序自动读取 `data/runtime.env` |
| 外网映射 | 开启，由 Nginx 反向代理处理 HTTPS |
| 数据库 | 默认本地 SQLite：`/www/wwwroot/ganwen-go/data/blog.db` |
| Nginx 配置模板 | [ganwen-go.bt.conf.example](../../deploy/ganwen-go.bt.conf.example) |

启动命令示例：

```text
/www/wwwroot/ganwen-go/ganwen serve --data /www/wwwroot/ganwen-go/data --media /www/wwwroot/ganwen-go/data/media --listen 127.0.0.1:8099 --preview=false
```

首次部署公网域名时，可附加一次 `--site-url https://blog.example.com` 保存站点地址。

## 新建步骤

1. 将程序与初始目录放到 `/www/wwwroot/ganwen-go/`，确保 `data/` 目录归属运行用户 `www` 且不映射到公网静态目录。
2. 在「网站 → Go项目 → 添加Go项目」填写执行文件路径、端口、运行用户和启动命令，勾选开机启动。
3. 绑定域名并开启「外网映射」，在「SSL」中部署有效 HTTPS 证书。
4. 在项目「配置文件」中参考 [ganwen-go.bt.conf.example](../../deploy/ganwen-go.bt.conf.example) 配置反向代理（替换其中的示例域名和证书路径）。不要附加旧 PHP 或 WordPress 伪静态规则；旧 `/?p=ID`、`/?page_id=ID`、`/index.php?p=ID` 链接由 Go 程序直接兼容。
5. 首次启动生成的随机管理员账号密码位于私密的 `data/admin-access.txt`，登录 `/admin/` 后可自行备份并妥善保管。

## 更新与回滚

1. 更新前先停止服务并完整备份 `data/` 目录（或使用 `ganwen database-export` 导出逻辑备份，切勿只复制运行中的单个 `blog.db` 而遗漏 WAL 文件）。
2. 上传新版本 Linux amd64 可执行文件到 `/www/wwwroot/ganwen-go/ganwen.next`，核对 SHA-256。
3. 在宝塔面板停止项目，备份旧版 `ganwen` 可执行文件，将 `ganwen.next` 替换为 `ganwen` 并设置 `755` 权限。
4. 启动项目并检查首页、文章页与后台登录。更新可执行文件不会重置文章、译文、媒体、账号或翻译配置。
5. 若启动异常，停止项目后换回备份的旧可执行文件即可恢复。
