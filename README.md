# 甘文博客 / Ganwen Blog

自己在用的 Go 博客程序，支持文章、独立页面、媒体、多语言和访问统计。默认使用 SQLite，也支持 MySQL。

[下载成品](https://github.com/ganwennl/ganwen-blog/releases/latest) · [使用说明 / User guide](docs/usage.md) · [开发说明](docs/development.md) · [更新日志](CHANGELOG.md)

## 运行

Windows 解压后运行 `Start.bat`；Linux 运行 `./start.sh`。打开 `http://127.0.0.1:8097/`，后台为 `/admin/`。首次启动的随机管理员密码保存在私有的 `data/admin-access.txt`。

## 项目目录

| 目录 | 内容 |
| --- | --- |
| `cmd/` | 博客程序和维护命令入口 |
| `internal/blog/` | Go 业务逻辑及相邻单元测试 |
| `internal/blog/web/` | 按 `admin/`、`public/` 分开的嵌入式页面、脚本和翻译 |
| `scripts/packaging/` | 构建、打包及受控更新工具 |
| `scripts/backup/` | 宝塔备份、异地备份与加密工具 |
| `scripts/migration/` | WordPress、媒体、译文和独立页面迁移 |
| `scripts/verify/` | 线上只读验证 |
| `tests/` | `scripts/` Python 回归、`client/` 前端测试、`package/` 成品验证 |
| `deploy/` | Nginx、systemd 等部署模板 |
| `packaging/` | 按 `windows/`、`linux/` 分类的成品启动脚本 |
| `docs/` | 用户、开发和 `deployment/` 部署文档 |
| `data/`、`migration/`、`dist/` | 私有运行数据、迁移材料和本地成品；不提交 Git |

## 开发

```powershell
.\scripts\test.ps1
go build -o dist/ganwen.exe ./cmd/ganwen
python scripts/packaging/build-release.py --output dist/releases/local
```

具体依赖和测试分类见[开发说明](docs/development.md)。数据与配置保持在自己的本地磁盘，升级与备份方法见[使用说明](docs/usage.md)。

## English

A personal Go blog with posts, standalone pages, media, translations and visitor statistics. Download a Windows x64 or Linux amd64 package, then follow the bilingual [user guide](docs/usage.md). Backend code, embedded web assets, operational scripts and tests are separated by purpose.

[MIT license](LICENSE) · [Third-party notices](THIRD_PARTY_NOTICES.md)
