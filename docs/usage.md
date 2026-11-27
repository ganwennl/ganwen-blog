# 甘文博客使用说明 / Ganwen Blog User Guide

我自己在用的个人博客程序，使用 Go 编写，单文件内嵌前后台页面，解压即可运行。支持文章、普通页面、独立页面、图片与 MP4 视频上传、多语言译文管理以及访问统计。界面自带 11 种显示语言，默认使用本地 SQLite，也支持 MySQL。

A self-hosted personal blog written in Go. Extract the archive and run it directly: no external Go, Node.js, or database runtime is needed for the default SQLite setup. It supports posts, pages, standalone custom-path pages, image/MP4 uploads, multilingual translations, and visitor analytics, with 11 built-in interface languages.

当前版本 / Current version: **v1.0.10** · [更新日志 / Changelog](../CHANGELOG.md)

## 下载运行

前往 [Releases](https://github.com/ganwennl/ganwen-blog/releases) 下载对应系统的压缩包（Windows x64 或 Linux amd64）。

### Windows

1. 将 `ganwen-blog-v1.0.10-windows-amd64.zip` 解压到可写的本地磁盘目录。
2. 双击 `Start.bat` 启动服务（保持窗口打开；关闭窗口即停止）。
3. 浏览器打开 `http://127.0.0.1:8097/` 查看前台，打开 `http://127.0.0.1:8097/admin/` 进入管理后台。
4. 首次运行会自动创建管理员账号 `ganwen`，随机密码保存在 `data/admin-access.txt`。

如需指定端口或数据目录，可在 PowerShell 中运行：

```powershell
.\Start.ps1 -Port 8098 -DataDirectory 'D:\MyBlogData'
```

也可以直接通过命令行初始化并启动：

```powershell
.\ganwen.exe init-admin --data data
.\ganwen.exe serve --data data --listen 127.0.0.1:8097
```

`init-admin` 仅用于首次生成账号，已有 `runtime.env` 时不会覆盖。执行 `.\ganwen.exe version` 可查看当前版本。SQLite 数据目录请放在本地磁盘，不要放在网络共享目录。

### Linux

```sh
unzip ganwen-blog-v1.0.10-linux-amd64.zip
cd ganwen-blog-v1.0.10-linux-amd64
chmod +x ganwen start.sh
./start.sh
```

默认监听地址与初始账号文件位置与 Windows 一致。可通过 `PORT=8098 DATA_DIR=/home/me/blog-data ./start.sh` 自定义端口与数据目录。

## 界面语言与文章译文

前台与后台内置 11 种界面语言：中文、荷兰语、英语、印地语、西班牙语、阿拉伯语、法语、孟加拉语、葡萄牙语、印尼语和乌尔都语（阿拉伯语和乌尔都语自动切换为从右到左排版）。界面语言无需配置外部翻译服务，也不会改动文章内容。

中文文章和页面支持 10 种目标译文，每份译文都可以单独手动编辑。如需调用外部兼容 API（如 OpenAI、DeepSeek 或兼容接口）辅助翻译，可在后台「翻译」页面自行配置服务地址、模型与密钥，支持自动翻译、手动点击翻译或暂停外部请求。人工修改过的译文默认受保护，不会被自动翻译覆盖。

## 内容管理与编辑

- **文章、页面与回收站**：后台列表支持一键公开、隐藏、在文章与普通页面之间移动（页面移为文章后原 `?page_id=` 链接自动 301 跳转），以及移入回收站、恢复为草稿或彻底删除。
- **独立页面**：可指定以 `/` 开头的自定义路径（如 `/about.html`），并可设为“仅通过地址访问”（不在首页、导航、搜索与站点地图中列出）。
- **格式清理**：“清除格式”作用于当前选区；“清除全部格式”清理整篇粘贴过来的多余字体、颜色、内联样式与外链，保留段落、图片和视频，并在继续编辑前支持一键撤销。

## 站点设置与公网部署

- **侧栏与归档**：「侧栏设置」可开关搜索、热门文章、站点统计、年月归档、分类、标签和广告位，支持拖拽调整顺序，并可控制文章阅读页是否显示右侧栏。
- **访问统计**：新安装默认展示真实统计数据；可在「站点设置」中按需隐藏统计组件或配置展示基数。后台「访问统计」始终记录真实访问，且不保存访客原始 IP。
- **联系方式**：粘贴邮箱或 HTTPS 链接可自动匹配图标，支持可选的联系前确认提示及多语言提示文案。

本地默认开启预览保护（`--preview=true`），只允许监听本机回环地址并输出 `noindex`。公网部署请先配置自己的 HTTPS 反向代理，再传入 `--preview=false` 和站点域名：

```powershell
.\ganwen.exe serve --data data --listen 127.0.0.1:8097 --preview=false --site-url https://blog.example.com
```

`--site-url` 会持久化写入站点设置，后续重启无需重复指定。

## 数据备份与升级

运行数据均保存在本地 `data/` 目录：
- `data/blog.db`：文章、译文、设置与访问统计
- `data/media/`：上传的图片和视频
- `data/runtime.env`：管理员密码哈希与数据库配置
- `data/translation.key`：外部翻译密钥的本地主密钥（配置过翻译服务时请务必一并备份）

升级前请先停止程序并完整备份 `data/` 目录（切勿在运行中只复制单个 `blog.db` 而遗漏 `-wal` 文件），然后替换新版 `ganwen` / `ganwen.exe` 启动即可。

如需跨机器或跨数据库（SQLite ↔ MySQL）迁移，可使用逻辑导出与导入：

```powershell
.\ganwen.exe database-export --data data --file backup\blog-database.json
.\ganwen.exe database-import --data restored-data --file backup\blog-database.json --key-file backup\blog-database.json.translation.key
```

`database-import` 要求目标库为空；`data/media/` 与 `data/runtime.env` 需另行复制。

---

## Download and Run

Download the `windows-amd64` or `linux-amd64` archive from [Releases](https://github.com/ganwennl/ganwen-blog/releases).

- **Windows**: Extract `ganwen-blog-v1.0.10-windows-amd64.zip` to a local disk folder and run `Start.bat` (or `.\Start.ps1 -Port 8098 -DataDirectory 'D:\MyBlogData'`). Visit `http://127.0.0.1:8097/` and sign in at `/admin/` using the initial `ganwen` credentials generated in `data/admin-access.txt`.
- **Linux**: Extract `ganwen-blog-v1.0.10-linux-amd64.zip`, run `chmod +x ganwen start.sh && ./start.sh` (or `PORT=8098 DATA_DIR=/home/me/blog-data ./start.sh`).

## Languages, Content, and Settings

- **Interface & Article Languages**: Public and admin pages include 11 built-in display languages (with RTL layout for Arabic and Urdu). Chinese posts and pages can hold up to 10 target translations—edited manually or generated through an optional OpenAI/DeepSeek-compatible HTTPS API configured in the admin panel. Human-edited translations are protected from automatic overwrite unless explicitly re-translated.
- **Content Management**: Manage posts, ordinary pages, and custom-path standalone pages (optionally unlisted). Lists support one-click publish/hide, moving between posts and pages (preserving old `?page_id=` links via 301 redirects), and a two-step recoverable trash. The editor includes selection formatting cleanup and whole-article paste cleanup with undo.
- **Sidebar, Contacts & Analytics**: Configure sidebar modules and drag-and-drop ordering, optional pre-contact confirmation notices, and real or baseline-adjusted public counters (admin analytics always tracks real visits without storing raw visitor IPs).
- **Public Deployment**: Local preview defaults to loopback with `noindex`. Behind an HTTPS reverse proxy, run with `--preview=false --site-url https://blog.example.com`.

## Backup, Upgrade, and Source Build

Stop the server and back up the entire `data/` directory (`blog.db`, `media/`, `runtime.env`, and `translation.key`) before replacing the executable, or use `database-export` and `database-import` for logical backups and SQLite/MySQL migration.

To build and test from source (requires Go 1.26+, Python 3 for packaging, and Node.js for client tests):

```sh
go test ./cmd/... ./internal/...
go build -o ganwen ./cmd/ganwen
python scripts/packaging/build-release.py --output dist/releases/v1.0.10
```

## 许可 / License

项目代码采用 [MIT License](../LICENSE)；第三方依赖保留各自原许可证，详见 [第三方说明](../THIRD_PARTY_NOTICES.md)。

Project code is licensed under [MIT](../LICENSE); third-party components retain their original licenses as listed in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).
