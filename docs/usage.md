# 甘文博客 / Ganwen Blog

这是我自己在用的博客程序，用 Go 写的，下载解压就能运行。可以写文章、做独立页面、上传图片和视频，也有访问统计。页面和后台自带 11 种显示语言，默认使用 SQLite，也支持 MySQL。

This is the blog software I use myself. It is written in Go: download, extract and run it. It supports posts, standalone pages, images, videos and visitor statistics. The public pages and admin interface include 11 display languages. SQLite is the default database; MySQL is also supported.

当前版本 / Current version: **v1.0.2** · [更新日志 / Changelog](../CHANGELOG.md)

## 下载运行

到 [Releases](https://github.com/ganwennl/ganwen-blog/releases) 下载适合自己的程序包。Windows 包适用于 x64；Linux 包适用于 amd64。程序不需要另装 Go、Node.js 或数据库服务，默认使用本地 SQLite。

### Windows

1. 解压 `ganwen-blog-v1.0.2-windows-amd64.zip` 到可写的本地目录。
2. 双击 `Start.bat`，保持打开的窗口；关闭窗口就停止服务。
3. 浏览器打开 `http://127.0.0.1:8097/`，后台是 `http://127.0.0.1:8097/admin/`。
4. 第一次启动会生成账号 `ganwen` 和随机密码，密码在 `data/admin-access.txt`。请保管好此文件。

换端口或数据目录可在 PowerShell 中运行：

```powershell
.\Start.ps1 -Port 8098 -DataDirectory 'D:\MyBlogData'
```

数据目录须放在本地磁盘，不放在网络共享中。若下载的脚本被系统拦截，先在文件属性中解除锁定，或直接使用下面的手动命令。

```powershell
.\ganwen.exe init-admin --data data
.\ganwen.exe serve --data data --listen 127.0.0.1:8097
```

`init-admin` 只用于首次安装；已有配置时会拒绝覆盖。查询版本使用 `.\ganwen.exe version`。

### Linux

```sh
unzip ganwen-blog-v1.0.2-linux-amd64.zip
cd ganwen-blog-v1.0.2-linux-amd64
chmod +x ganwen start.sh
./start.sh
```

地址、初始账号和数据文件与 Windows 相同。可用 `PORT=8098 DATA_DIR=/home/me/blog-data ./start.sh` 换端口和目录。

## 显示语言

前台和后台支持中文、荷兰语、英语、印地语、西班牙语、阿拉伯语、法语、孟加拉语、葡萄牙语、印尼语和乌尔都语。后台右上角可以切换“界面语言”，会记住这台浏览器的选择；当前语言排在语言列表第一位。阿拉伯语和乌尔都语使用从右到左的排版。

显示语言自带在程序里，不需要翻译服务或 API Key。它只影响菜单、按钮和操作提示。文章及已有译文仍按原来的内容语言管理，切换界面不会翻译或修改文章。

## 设置和自己的站点地址

后台“站点设置”可以修改标题、描述、布局、联系信息和虚拟访问量。新安装默认使用真实访问量；“启用虚拟访问量”开启后才使用展示用计算，可设置每日基数（0–1000000）。关闭该开关会停止主页总数和热门文章的虚拟计算。后台访问报告一直统计真实记录。

“侧栏设置”独立管理搜索、热门文章、站点统计、年月归档、分类、标签和广告，也可选择是否在文章页显示侧栏。勾选所需组件并保存，再点击“预览网站”查看效果；全部关闭后正文使用完整宽度。年月归档按文章发布日期生成，展开年份可选择全年或具体月份。已有站点保留原统计开关，并默认显示其他组件。

本地运行默认只监听本机，并禁止搜索引擎收录。对外使用时，请先配置自己的 HTTPS 域名及反向代理，再按自己的环境运行：

```powershell
.\ganwen.exe serve --data data --listen 127.0.0.1:8097 --preview=false --site-url https://blog.example.com
```

`--site-url` 会保存站点地址；后续启动不用重复设置。翻译服务是可选功能，可在后台配置兼容的服务地址、模型和密钥；没有配置时可以正常写中文文章，不会自动得到译文。

## 数据、备份和升级

`data/blog.db` 保存文章、设置和统计；`data/media` 保存上传文件；`data/runtime.env` 保存管理员配置；使用翻译功能时还需要保留 `data/translation.key`。这些都是自己的数据，不包含在下载包里，也不要提交到公开仓库。

升级前先停止程序，把整个 `data` 目录复制到自己的备份位置。解压新版本到另一个目录，将原 `data` 复制过去再启动；自定义数据目录直接继续指定原路径。不要覆盖数据文件，也不要只复制正在运行的 SQLite 主文件。回退时使用旧程序和升级前的数据副本。

也可以使用逻辑导出：

```powershell
.\ganwen.exe database-export --data data --file backup\blog-database.json
.\ganwen.exe database-import --data restored-data --file backup\blog-database.json --key-file backup\blog-database.json.translation.key
```

导入目标必须为空。导出保留文章、设置、译文、队列和统计，但媒体、管理员配置要另行复制；有翻译密钥时需单独保留生成的 `.translation.key` 备份。MySQL 可在私有 `data/runtime.env` 中指定 `GBLOG_DATABASE_DRIVER=mysql` 与 `GBLOG_DATABASE_DSN`，数据库服务需要自己准备。

## 源码开发

需要 Go 1.26 或更新版本；构建发布包还需要 Python 3。Node.js 用于前端脚本测试。

```sh
go test ./cmd/... ./internal/...
go build -o ganwen ./cmd/ganwen
python scripts/packaging/build-release.py --output dist/releases/v1.0.2
```

构建脚本只打包程序、启动脚本、说明和许可，不复制本地数据库、媒体或配置。

## Download and run

Download a package from [Releases](https://github.com/ganwennl/ganwen-blog/releases). The Windows package targets x64 and the Linux package targets amd64. Go, Node.js and a database server are not required to run the default SQLite setup.

On Windows, extract the ZIP to a writable local folder and double-click `Start.bat`. Keep the console open while using the blog. Open `http://127.0.0.1:8097/`, then visit `/admin/` to sign in. The first run creates the `ganwen` account and a random password in `data/admin-access.txt`. Keep that file private.

Use `.\Start.ps1 -Port 8098 -DataDirectory 'D:\MyBlogData'` to change the port or data directory. Keep SQLite data on a local disk. If Windows blocks a downloaded script, unblock it in file properties or use the manual `init-admin` and `serve` commands above. Initialization refuses to replace existing credentials. Run `ganwen.exe version` to check the version.

On Linux, extract the ZIP, enter its folder, run `chmod +x ganwen start.sh`, then `./start.sh`. Use `PORT=8098 DATA_DIR=/home/me/blog-data ./start.sh` for another port or data location.

## Display languages

The public pages and admin interface support Chinese, Dutch, English, Hindi, Spanish, Arabic, French, Bengali, Portuguese, Indonesian and Urdu. Use the language selector in the top-right corner of the admin page. Your browser remembers the choice, and the selected language comes first in the list. Arabic and Urdu use a right-to-left layout.

Interface translations are included in the program and need no translation service or API key. They cover menus, buttons and messages. Posts and their existing translations keep their own content languages; changing the interface language does not translate or edit articles.

## Settings, backup and development

Fresh installations display recorded view counts. The admin site settings let you enable virtual counts and set their daily display target (0–1000000). Turning virtual counts off uses real counts for both totals and popular articles. Admin analytics always uses real records.

The dedicated sidebar settings page controls search, popular articles, site statistics, date archives, categories, tags and ads, as well as sidebar visibility on article pages. Save your selection and use “Preview website” to check it. Disabling all components gives the content the full width. Date archives use publication dates; expand a year to browse the full year or a particular month. Existing sites retain their statistics visibility setting and show the other components by default.

Local preview listens on loopback and prevents indexing. For a public site, set up your own HTTPS domain and reverse proxy, then use `--preview=false --site-url https://blog.example.com`. The URL is saved for later runs. Translation is optional and requires your own compatible service configuration in the admin interface.

Before upgrading, stop the program and back up the entire `data` directory. Extract the new program separately and copy your existing data into it, or keep using your custom data path. Do not replace data with package files or copy only the live SQLite main file. To roll back, use the old program with the data copy made before upgrading.

`blog.db` stores content, settings and analytics; `media` contains uploads; `runtime.env` contains admin configuration. Preserve `translation.key` if you use encrypted translation credentials. The `database-export` and `database-import` commands above provide logical backups. Imports require an empty target. Copy media and admin configuration separately, and keep the separate key backup when one is generated. MySQL requires your own database service and private `GBLOG_DATABASE_DRIVER` / `GBLOG_DATABASE_DSN` settings.

For development, use Go 1.26 or later. Python 3 builds release packages; Node.js runs frontend tests. Run the source commands above. The packager copies only program files, launchers, documentation and licenses.

## 许可 / License

我自己的代码使用 [MIT](../LICENSE)。第三方组件保留原许可，见 [第三方说明](../THIRD_PARTY_NOTICES.md)；程序包附带实际编译依赖的许可文本。

My own code is licensed under [MIT](../LICENSE). Third-party components retain their original terms; see [third-party notices](../THIRD_PARTY_NOTICES.md). Release packages include the licenses for their compiled dependencies.
