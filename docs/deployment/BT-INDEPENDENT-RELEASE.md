# 宝塔独立证据页维护 helper

仅用于已授权的本次发布。helper 不启动、停止或重启服务；`ganwen_blog` 始终通过宝塔 Go 项目控制器管理。维护计划任务目前作为临时手动执行入口启用，发布完成或中止后立即暂停，不保留自动发布任务；仅执行操作方已核对的当前阶段。

已确认路径：

- 程序 `/www/wwwroot/ganwen-go/ganwen`；SQLite `/www/wwwroot/ganwen-go/data/blog.db`；媒体 `data/media`；运行用户 `www`。
- Nginx `/www/server/panel/vhost/nginx/go_ganwen_blog.conf`；检查程序 `/www/server/nginx/sbin/nginx`。
- 私密维护目录 `/www/backup/ganwen-maintenance-20261013`。
- 上传包 `incoming-independent-sidebar-20261013.tar.gz`，SHA-256 `200b46028b1b235f715ad214012592b296c4b4af113b6e6a5c9bc421bab8480e`。
- 首次发布前程序 SHA-256 `ea63350988fb0b43f48ff0f21ccf8f9bc40fe0d07e73c53aa502c069ceed2dc1`；首次执行不一致时停止处理并查明，不自行覆盖。

将 `scripts/packaging/release-independent-page.py` 上传为维护目录内的 `release-independent-page.py`。宝塔任务使用已确认可用的 Python 3，以下 `python3` 应替换成该任务原先已验证的绝对 Python 路径；不要新装定时部署任务。

```sh
python3 /www/backup/ganwen-maintenance-20261013/release-independent-page.py stage
```

`stage` 核对上传包及旧程序指纹，安全解包并逐文件检查，按当前 Nginx 配置生成 `nginx.next.conf`。只改证据页及媒体相关 location；SSL、监控 include、域名和其他项目配置保留。不会修改生产程序或数据库。JSON 结果须为 `ok=true, stage=staged`。

在宝塔停止 `ganwen_blog`，确认暂停后执行：

```sh
python3 /www/backup/ganwen-maintenance-20261013/release-independent-page.py install
```

`install` 要求 8099 端口及原程序进程均停止，保存旧程序、Nginx、运行配置和一致 SQLite 快照；导入完整原文与 24 个证据，核对其他文章、配置、翻译任务及密钥不变，设置新增媒体和数据库侧文件为原 `data` 属主，原子替换程序与已审查配置，执行 `nginx -t`。任一步失败会恢复上线前数据库、程序及 Nginx，并保留失败时数据副本；新增证据文件保留，不删除任何原文件。

结果须为 `ok=true, stage=installed`。随后通过宝塔启动 Go 项目、重载 Nginx，再执行：

```sh
python3 /www/backup/ganwen-maintenance-20261013/release-independent-page.py verify
```

`verify` 核对当前程序、数据库和 24 个媒体 SHA，检查本机中文/荷兰语独立页、noindex、首页及站点地图隐藏、媒体 HEAD 和视频 Range，并再次检查 Nginx。公网、旧文章及后台验收由发布操作方完成；helper 的本机结果不等于公网证据。

需要回滚时先通过宝塔停止 Go 项目，然后执行：

```sh
python3 /www/backup/ganwen-maintenance-20261013/release-independent-page.py rollback
```

helper 先保存最新数据库、程序和配置，再恢复上线前一致快照及旧程序、Nginx。旧版程序不认识隐藏页面，不能只回退程序而保留新增证据页记录。回滚后通过宝塔启动旧项目并重载 Nginx，再验收原博客及旧静态证据页。

所有副本、状态和日志只留在服务器私密维护目录，不上传到 Git或公开站点；helper 只输出阶段、数量、成功与否，不输出配置、密钥、原文或子进程日志。重复 `stage/install` 不重复导入已完成发布。

## 证据页语言程序更新

独立页首次发布后的程序 SHA-256 为 `25a37aea40cbc42f7f33c80e277fa32676053c871ca95d405499333a02efea19`。语言更新使用单独的 `scripts/packaging/release-program-update.py`，仅替换程序，保留数据、媒体、Nginx 及首次发布状态。它核对固定目录、旧/新指纹、Linux amd64 ELF、停止状态和原权限，保存私有旧程序及独立 `program-update-state.json`；安装失败恢复旧程序。

先上传 helper 与已核对的 `incoming-evidence-language-20261013.bin` 至同一私密维护目录，在宝塔停止 Go 项目，再执行：

```sh
python3 /www/backup/ganwen-maintenance-20261013/release-program-update.py install --binary incoming-evidence-language-20261013.bin --expected-current 25a37aea40cbc42f7f33c80e277fa32676053c871ca95d405499333a02efea19 --expected-new dc2b678a7fd0c65ab67475cadc5c4eecdb61098750d6f7439ad26e73b7962c37
```

通过宝塔启动项目后，用相同指纹参数执行 `verify`，并验收公网。需要回退这次语言更新时，在宝塔停项目后将 `install` 改为 `rollback`，恢复仅程序，再启动；不要调用首次独立页导入的完整数据回滚。

证据页按浏览器首选 `Accept-Language` 判断，显式 `Hans/Hant` 脚本优先，无脚本时按 `CN/SG` 与 `TW/HK/MO` 区分；仅简体中文打开中文，其他或空值默认荷兰语。证据页忽略全站语言 cookie，中文切换使用 `evidence_lang=zh`，`/nl/` 是明确荷兰语。身份来自保留的双语原始 HTML，后台修改地址或正文不影响规则；页面 ID 入口同样生效。普通博客、canonical 和 hreflang 保持原规则。没有可公开荷兰语译文时保留可访问的中文，避免跳到不存在的页面。

本次程序更新 helper 的 10 项测试在本机与真实服务器 Python 3.7 上通过，全部 Go 源码通过 `go test ./cmd/... ./internal/...`；公网 28 个语言及入口 case 通过，覆盖独立地址和 ID 别名、简体/繁体及显式脚本优先、非中文/空语言、旧博客 cookie 和手动中荷切换。

## 实际上线与备份边界（2026-10-13）

语言程序已通过宝塔实际上线，当前生产程序 SHA-256 为 `dc2b678a7fd0c65ab67475cadc5c4eecdb61098750d6f7439ad26e73b7962c37`。证据页完整中文及原始荷兰语、24 个媒体均已迁入博客并核验；匿名访问 noindex，首页、搜索和站点地图隐藏，有返回博客链接。独立证据页侧栏默认关闭，后台可调整。

配套备份最终源码已验证：原生任务含 legacy SSL 分类修复的 45 项在服务器全部执行通过；补充上传 44 项通过；项目补充备份服务器 59 项中跳过 1 项 Windows 权限测试，实际 58 项通过。原生模板缺失、分钟配额/TLS 分类、失败归档硬链接保留和精确匹配重试，以及补充最终扫描的安全分类/路径 hash 边界见 [自动备份文档](BT-AUTO-BACKUP.md)。历史 `final_inventory` 缺失文件未保存 filename，不能断言属于缓存或 SQLite。

目前 MySQL 8/8 已成功；网站初次 ALL 的 6/8 结果保留，另 2 个目标已单独补验证，本日历史 8/8 已确认。旧 `ganwen.nl` 网站删除登记后当前为 7 个项目，均有已验证备份，旧站历史包仍保留；`xboardapi` 大包精确匹配复用、回读成功，没有重压或重传。37 份旧发布归档已移至私密维护目录，源 `backups` 保留最新 3 份，未删除旧版本或声称释放磁盘。

后续补充作业在 `stage_files` 缺失旧站证书私钥，原因已确认为站点删除令长作业清单失效；Go SSL 使用正常存在的 `cert/ganwen_blog` 证书与私钥，不恢复已废弃的旧证书，不按缓存忽略。继续用已验证脚本重新读取当前登记和源目录后重跑，详情见自动备份文档。

发布前 13:05 的旧补充密文已成功上传，但不能作为本次上线后的证据页或当前程序备份。发布后补充密文正在重跑，尚未确认成功，最终回执待填写。
