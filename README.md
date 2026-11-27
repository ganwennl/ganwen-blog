# 甘文博客 / Ganwen Blog

自己日常使用的个人博客程序，基于 Go 编写，单二进制内嵌前后台资源，解压即可运行。支持文章、普通页面、独立地址页面、图片与 MP4 视频上传、十种目标译文管理以及本地访问统计。默认使用 SQLite，也支持独立 MySQL 数据库。

[下载版本](https://github.com/ganwennl/ganwen-blog/releases/latest) · [使用说明 / User Guide](docs/usage.md) · [开发说明](docs/development.md) · [更新日志](CHANGELOG.md)

## 快速运行

下载对应平台的压缩包并解压：
- Windows：运行 `Start.bat`（或 `Start.ps1`）
- Linux：运行 `./start.sh`

浏览器访问 `http://127.0.0.1:8097/`，管理后台为 `http://127.0.0.1:8097/admin/`。首次运行会在本地 `data/admin-access.txt` 生成随机管理员密码。

## 目录结构

| 目录 | 说明 |
| --- | --- |
| `cmd/ganwen/` | 命令行与 HTTP 服务入口 |
| `internal/blog/` | 文章存储、权限校验、翻译队列、访问统计及单元测试 |
| `internal/blog/web/` | 内嵌的前台模板（`public/`）与管理后台（`admin/`） |
| `scripts/packaging/` | 发布包与站点打包脚本 |
| `scripts/backup/` | 站点备份与加密上传脚本 |
| `scripts/migration/` | WordPress 内容、媒体与独立页面导入脚本 |
| `scripts/verify/` | 站点只读验收脚本 |
| `tests/` | Python 脚本测试、Node.js 前端测试与打包冒烟测试 |
| `deploy/`、`packaging/` | Nginx / systemd 配置模板与启动脚本 |
| `docs/` | 使用说明、开发说明与部署参考 |

`data/`、`migration/`、`dist/` 为本地运行或构建目录，已在 `.gitignore` 中忽略。

## 本地开发

```powershell
.\scripts\test.ps1
go build -o dist/ganwen.exe ./cmd/ganwen
python scripts/packaging/build-release.py --output dist/releases/local
```

依赖环境与测试说明见[开发说明](docs/development.md)，配置、备份和升级方式见[使用说明](docs/usage.md)。

## English

A self-hosted personal blog written in Go, bundling public templates and the admin interface into a single executable. It supports posts, pages, custom-path standalone pages, image/video uploads, multilingual translations (11 UI display languages and 10 article target languages), and privacy-preserving visitor analytics on SQLite or MySQL.

Download a Windows x64 or Linux amd64 archive from [Releases](https://github.com/ganwennl/ganwen-blog/releases/latest) and see the bilingual [user guide](docs/usage.md) for setup, backup, and upgrades.

[MIT License](LICENSE) · [Third-party notices](THIRD_PARTY_NOTICES.md)
