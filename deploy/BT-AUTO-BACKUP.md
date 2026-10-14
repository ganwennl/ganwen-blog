# 宝塔全部项目自动备份

服务器 `66.42.81.91`，宝塔服务器时区 `UTC+8`。全部项目由三个服务器端任务共同覆盖，不依赖本机 Codex 开机。

| 任务 | 宝塔时间 | 目标 / 保留 |
| --- | --- | --- |
| 全部网站备份[原网盘路径·3650份] | 每天 01:40 | 原 Google Drive 网站目录，每个项目 3650 份 |
| 全部MySQL备份[原网盘路径·3650份] | 每天 04:00 | 原 Google Drive SQL 目录，每个数据库 3650 份 |
| 全部项目补充备份[谷歌网盘·3650份] | 每天 04:30 | 下方私有目录，3650 份；本机额外保留 7 份已验证密文 |

补充任务自动发现 `/www/wwwroot` 下未登记在宝塔网站列表的目录，扫描全部项目中的 SQLite。旧 `ganwen.nl` 网站及其库删除登记后，当前登记 7 个项目、7 个 MySQL 库；本日原 8 个网站和 8 个库的历史备份仍保留。此前额外补齐 `ppanelapi`、`ppanel-user-web`、`ppanel-admin-web`、`www.ffpay.lol`、`pf.ffpay.lol`、`default`、`java_node_ssl` 七个目录，当前补充范围以重新扫描的 manifest 为准。空目录也记录，后续新增目录自动纳入。宝塔 ALL 网站支持 Go，程序路径注册为文件时备份其父目录；原任务不提供 SQLite 在线一致快照。

## 补充包

[Google Drive：原 ganwen.nl 目录](https://drive.google.com/drive/folders/1tNW8vhlZx5HB3ecV4fx1WxnHn26o4JX0)，仅 `ganwen33@gmail.com` 所有者访问。新博客及补充包沿用原目录，其他项目和 SQL 备份保持各自原路径；复用现有宝塔 Google Drive 授权，不增加权限范围。

服务器脚本目录 `/www/server/panel/script/ganwen-project-backup/`，本机应急密文目录 `/www/backup/project-supplement/`，目录权限 700，包和回执权限 600。文件名为 `bt-project-supplement-时间-随机值.tar.gz.enc`，相邻 `.json` 为大小、MD5、SHA-256 和网盘文件 ID 回执。

补充包包含：遗漏的项目文件；所有 SQLite 的 `Connection.backup()` 在线快照和完整性检查；独立完整的 Ganwen 程序、媒体、配置和逻辑数据库；宝塔 vhost、`site.db` / `database.db` / `crontab.db` 在线快照及恢复路径映射。Ganwen 导出在临时空库中重新导入，逐项比较文章、设置、翻译队列、访问统计和密钥指纹，不启动翻译服务。旧版不含统计的逻辑备份仍可恢复。

上传前使用 AES-256-GCM 和 RSA-OAEP-SHA256 整体加密；服务器只保存公钥。上传后通过 Drive API 回读大小、MD5、父目录、隐私及归属标记，验证通过才记录成功。本地清理只针对已上传且回执和文件指纹一致的本任务密文；未上传成功的密文不清理。云端超出 3650 份时将最旧包移入回收站，不永久删除。补充任务也核对现有网站/SQL备份的 3650 份保留，避免安装版本网盘插件的清理问题。

原生网站和 MySQL 包继续采用原来的压缩格式，未改成加密包。旧「备份网站[ 所有 ]」和「自动备份mysql数据库[所有]」任务暂停并保留，作为新任务的配置模板，请勿删除或改成 Shell 类型。新任务复用宝塔 `panelBackup` 和已安装 Drive 插件，只在本次进程适配目录、限流重试及上传校验，不改插件文件或授权。`ganwen_blog` 沿用原 `ganwen.nl` 目录；`language` 网站和同名 MySQL 库继续共用原网站网盘目录，并分别按网站/SQL文件名保留版本。三个任务的执行时刻不同，不声称整个服务器跨项目事务一致。

网站和 MySQL 的 Drive 操作执行入口间隔至少 1.1 秒；SDK 内部分片仍沿用原插件行为。明确的每分钟请求配额与临时错误最多请求 7 次，累计退避覆盖 63 秒；存储额度、每日额度及权限拒绝不按限流重试。已识别的 TLS EOF、bad record MAC 等传输中断继续同一请求有限重试；证书校验失败、协议错误及未知 SSL 原因直接失败，TLS 校验不关闭。全部目标逐一回读大小、MD5、父目录及归属/隐私，任一失败均返回非零，不能仅凭宝塔「正常」状态或末尾 `Successful` 判断备份成功。

兼容 Python 3.7 包装器把标准 SSL 原因仅保存在 `args[1]` 的情况：只提取白名单 `[SSL: TOKEN]` 或固定标准 EOF 前缀，不记录 raw 消息、URL 或任意 token。证书错误优先拒绝，未知 token、协议错误及冲突标记不按已知传输中断重试；复用相同请求，不改变证书校验。

三个 Shell 任务将 `set -eu` 放在子 Shell 内，并用 `) || exit $?` 向宝塔传递失败，避免 `set -u` 影响宝塔脚本末尾的参数。补充备份失败保留权限 700 的私密诊断目录及固定 `phase/code/error_type`，成功才清理。可定位的文件错误另记录来源类别、文件类型及路径 SHA-256；日志和 `failure.json` 不输出未知完整路径、私人文件名、库异常原文或凭据。加密完成后移除该任务额外的明文归档，未上传的密文仍保留。

普通项目文件仍作一致性检查。最终扫描只容忍初次清单已确认属于原生网站任务覆盖范围、非 Ganwen、非 SQLite 的明确运行缓存文件消失；不会跳过缓存目录扫描，新增 SQLite 仍阻断发布。主项目根目录、配置、原媒体（含 uploads/media/evidence 下无扩展名对象）、数据库及未覆盖源文件保持严格检查；本轮已读到 SQLite 头的文件不能再按缓存忽略。已知 SQLite 的临时 WAL/SHM/journal 不归档，可在最终扫描中消失；明确的 `.log` 文件取打开时长度的快照前缀并记录，防止日志持续追加阻塞自动任务。

## 手动执行与检查

在宝塔「计划任务」执行补充任务并查看日志。也可在宝塔终端执行：

```bash
btpython /www/server/panel/script/ganwen-project-backup/bt-offsite-run.py --check-only
btpython /www/server/panel/script/ganwen-project-backup/bt-project-backup.py --dry-run
btpython /www/server/panel/script/ganwen-project-backup/bt-offsite-run.py --cloud-keep 3650 --local-keep 7
btpython /www/server/panel/script/ganwen-project-backup/bt-native-all.py --save 3650 --check-only
btpython /www/server/panel/script/ganwen-project-backup/bt-native-all.py --kind database --echo bbda38b433e8dfb4e52b688eb69674fd --save 3650 --check-only
```

若归档和加密已完成、仅上传失败，可在同一备份目录选择已有 `.tar.gz.enc`，执行 `bt-offsite-run.py --retry-encrypted <密文绝对路径>`，跳过项目扫描和加密。该模式校验目录边界、固定文件名及加密头；同名云对象须精确匹配才复用，冲突拒绝。已有本地回执时只核验旧云文件 ID，不因列表暂缺而创建重复对象。SSL 传输中断按同一请求有限重试，证书校验仍保持开启。

原生网站可使用 `bt-native-all.py --site language --save 3650` 只补一个唯一登记的网站；未登记、重名及越界路径拒绝，MySQL 模式仍只执行 ALL。网站上传失败时，将已完成的原压缩包硬链接到该网站原生备份目录下的 `.failed-uploads/`，核对同一 inode；目录 700、文件 600，拒绝 symlink、越界及不同文件的同名覆盖。该链接在原生清理原包后仍保留；保留失败会明确记录 `archive_retained=false`，不能声称文件已安全保留。

执行 `bt-native-all.py --retry-archive '<已保留原生归档的绝对路径>' --save 3650` 可直接上传已有归档，不重新压缩；多个站点可重复传入 `--retry-archive`。路径必须属于固定原生备份根、唯一登记站点及规定的归档文件名。同名云文件只在唯一匹配并回读 ID、大小、MD5、原父目录、所有者及未共享/未删除状态全部一致时复用；冲突或歧义拒绝新建。该模式只报告 `NATIVE_RETRY_SUCCESS` 的指定目标，不能当作 ALL 成功；失败归档不会自动删除。

补充成功日志含 `BACKUP_SUCCESS`、云文件 ID、校验值和保留数量；原生任务每个目标含 `NATIVE_UPLOAD_VERIFIED`，全部完成才含 `NATIVE_BACKUP_SUCCESS`。失败返回非零，保留本地密文，不打印密钥或库异常里的凭据。网站和 MySQL 共用进程锁，手动执行须顺序进行。强制断电/终止可能留下私有临时目录，恢复后应核对并清理本任务临时文件。

## 恢复

恢复私钥保存在本机：`C:\Users\Gebruiker\Documents\宝塔项目备份恢复密钥-20261007.pem`，项目 `data/offsite-backup-recovery.pem` 有第二份，仅当前 Windows 用户可读。私钥不上传服务器或网盘；需另行保存离线副本，丢失后无法解密补充包。

先从 Drive 下载需要的 `.enc`，在本机解密为一个不存在的新文件：

```powershell
Set-Location 'D:\newcode\ganwenblog'
python -X utf8 scripts/bt-backup-crypto.py decrypt --input 'D:\恢复\备份.tar.gz.enc' --output 'D:\恢复\备份.tar.gz' --key 'C:\Users\Gebruiker\Documents\宝塔项目备份恢复密钥-20261007.pem'
```

解密校验通过后，解压到隔离目录，先查看 `manifest.json`。`restore_entries` 记录原绝对路径、权限、UID/GID；`sqlite_snapshots` 为一致数据库，不能再覆盖原 WAL/SHM/journal。Ganwen 优先使用 `ganwen/database.json` 和相邻密钥，通过 `database-import` 恢复到新目录；命令不会启动 worker。上线恢复前先备份当前数据并在宝塔停止对应项目，再恢复程序、媒体、配置及数据库并启动。

其他登记项目文件用原 ALL 网站包，MySQL 用原 ALL SQL 包，通过宝塔导入；先在隔离实例验证。`panel-metadata` 仅供恢复项目设置时隔离读取参考，不直接覆盖正在运行的宝塔数据库。此次未执行所有 MySQL 库的完整实例恢复。

## 实际验证（2026-10-07）

补充包已实际上传至原 `ganwen.nl` 目录，文件 ID `199GtYtYEjlEgN5O32GlcJsPg4oR3Ki32`，221704854 字节。云端重新下载后 SHA-256 为 `ab109cd1367120ffa3c37a63660116bfb9350fa2b8bf7d49eb678fb7374dfaab`，本机解密成功，再导入隔离 SQLite：97 篇文章、3 项配置、18 条翻译任务逐项一致，密钥指纹一致，`integrity_check=ok`，未启动 worker。包记录 8 个登记项目、7 个补充目录、1 个项目 SQLite 和 3 个宝塔元数据库。

新博客原生网站包已单站实际上传并回读校验，文件 ID `1nrN90JVzN1-b6lMrGl_nlCB8L67d2cal`，94586727 字节，父目录为旧 `ganwen.nl`；其他 7 个网站及 8 个 MySQL 库核对原任务最近成功日志与网盘文件。未再次手动重跑全部约 6 GiB 网站包。最终服务器 Python 3.7 的 45 项文件/快照、26 项上传/保留、13 项原生目录兼容测试全部通过；本机加密专项 3 项通过。

## 本次修复验证（2026-10-13）

原生网站模板原记录缺失，不能把新 Shell 任务自身当作原生 ALL 配置；已通过宝塔恢复原生网站模板并暂停保留。MySQL 原任务也暂停保留，由每天 04:00 的新任务调用原生备份。分钟请求配额仅在明确分类时重试，TLS 传输中断与证书/协议/未知错误分别处理，未降低校验要求。

最终源码验证如下；测试通过与真实备份任务的终态分别记录：

| 验证范围 | 已确认结果 |
| --- | --- |
| `bt-native-all.py` | 最终 legacy SSL 分类修复已通过宝塔安装，服务器 Python 3.7 全部执行 46 项，通过 |
| `bt-offsite-run.py` | 44 项，通过 |
| `bt-project-backup.py` | 服务器 Python 3.7 共 59 项，跳过 1 项 Windows 权限测试，实际执行 58 项，通过 |
| 程序更新 helper | 本机和服务器 Python 3.7 的 10 项，通过 |
| Go 全部源码 | `go test ./cmd/... ./internal/...` 通过 |
| 公网证据页语言及入口 | 28 个 case，通过 |

发布后补充任务曾在 `final_inventory` 报 `required_path_missing / FileNotFoundError`。旧诊断没有保存 filename，且当时初始全清单未留存；只读核对 13,906 个已复制源项目前缺失和类型变化均为 0，不能追认历史消失文件为缓存或 SQLite。修复通过隔离测试复现并处理原生覆盖运行缓存的 stat/open 删除竞态，增加安全分类和路径 hash；关键源文件的缺失仍失败。

后续 `batch155631` 在 `stage_files` 再报缺失，安全路径 hash 为 `2ddc005bbfaaea212f78f7d5041d3a15122eebe0863469c82412074065f68672`，已精确定位为旧站 `cert/ganwen.nl/privkey.pem`。该站在作业期间被实际删除登记，对应证书目录移除，此前采集的清单失效；这次是站点删除，不能归因为缓存或临时配置。当前 Go 的 SSL 使用 `cert/ganwen_blog`，其证书和私钥均存在，不恢复已废弃的旧私钥。

本次不增加 vhost 快照策略，继续使用已验证的项目脚本 SHA-256 `6b1ea172fdbeee5b4ac152b45548ab01170999eb0e98a82a994d71a87fd7d48a`。新进程重新读取当前登记与文件清单后重跑。作业会冻结起始站点列表并检查文件根目录，不能据此声称已完成对结束时最新登记的事务一致性检查；站点变更导致失败后须重新读取再运行。

当前真实备份进度：8/8 个 MySQL 库已上传并逐项回读校验，独立 Drive 连接器已核对大小、原父目录及未共享状态。网站初次 ALL 的 6/8 结果及失败记录保留，另外 2 个目标随后单独补验证成功，本日原 8 站历史备份已确认 8/8；旧站删除后当前登记的 7 个项目均有已验证备份，旧站历史包仍保留。

`xboardapi` 的已有大包按精确匹配复用并回读成功：`NATIVE_ARCHIVE_REUSED` 文件 ID `14x6An9QawnT7OcEM4_BufaKezwqvoG_G`，6459902838 字节，原父目录 `1L75YM_tJuG-bv6_jpBETvi7MGg1MCFL8`；`NATIVE_RETRY_SUCCESS` 为指定目标 1 个。这次没有重压或重传该大包，单目标补验证不改写初次 ALL 的失败结果。

已实际将源项目 `backups` 中 37 份旧发布归档、共 6736957698 字节移入 `/www/backup/ganwen-maintenance-20261013/xbgo-old-release-archives`，原目录 40 份中保留最新 3 份；操作结果为 `HISTORY_MOVE_SUCCESS`。旧归档完整保留在私密维护目录，未永久删除，目录移动不表示释放磁盘空间。

发布前 13:05 旧补充归档曾在加密后遭遇 SSL 传输错误；原密文保留并用新重试功能成功上传。云文件 ID `18J3V4WL7H_IDzmBdAlZHA8ugjSZf9XtM`，259079878 字节，SHA-256 `9e188426ab0d3605d3a2ef6670f3a7077df1cc3bac95abf33c7e5e68b88d9023`。该包是博客发布前快照，不能当作发布后证据页和当前程序的数据备份。

发布后最新补充包已于 `2026-10-13T16:31:06Z` 完成 `BACKUP_UPLOAD_VERIFIED` 和 `BACKUP_SUCCESS`。文件 `bt-project-supplement-20261013T162046Z-2e93d0b3804e.tar.gz.enc`，云 ID `1-TaoDQUGm2V2SzKc4DttNMMeEmxMViyT`，329065070 字节，MD5 `5a089f116789edb95a07e36897a55318`，SHA-256 `2e47775ccdfa096b0185ef126a6317ef14d7ed0177d177b74db91c2e21f05574`。独立 Drive 连接器确认原父目录及未共享状态，Windows 原 `G:\我的云端硬盘\bt_backup\web\ganwen.nl` 目录已可见该文件。

本轮清单包含当前 7 个登记项目、7 个补充目录、1 个项目 SQLite 和 3 个宝塔元数据库；Ganwen 包内程序 SHA 精确等于已上线 `dc2b678a7fd0c65ab67475cadc5c4eecdb61098750d6f7439ad26e73b7962c37`。上传中出现一次 TLS 中断，同一请求重试后成功；两次历史失败保留在私密验收记录，没有改写为成功。

从 Windows 原网盘目录全量下载至本机私密验证目录，329065070 字节及 SHA-256 与服务器成功回执完全一致。服务器打包前已自动完成 Ganwen 逻辑导出及隔离导入比较；本机解密验证遇到恢复私钥的 Windows 读取权限拒绝，当前不能声称已完成本机解密恢复。

## 安全验证边界

Ganwen 认证、会话、CSRF、上传、私密文章、HTML过滤、出站限制和密钥隔离共 21 项专项测试通过。已验证匿名后台隔离和禁止索引/缓存；这不等于完成整台服务器漏洞扫描。后台尚未实现两步验证，宝塔管理入口当前使用 HTTP，TLS旧协议配置另需完整核对。

官方说明：[宝塔计划任务](https://docs.bt.cn/user-guide/crontab/task/)、[SQLite在线备份](https://docs.python.org/3/library/sqlite3.html#sqlite3.Connection.backup)、[Drive文件校验字段](https://developers.google.com/workspace/drive/api/reference/rest/v3/files)。

## 每日 MCP 检查（2026-10-13 设置）

本机已创建每日 09:00（Europe/Amsterdam）的检查任务，使用 Xterminal 和 Google Drive MCP 对当前登记目标检查真实成功回执及云文件。失败时诊断，并把对应保留归档下载至当前 Windows 桌面的「宝塔备份兜底/日期」目录，校验大小和 MD5/SHA-256。正常时安静，失败或本地兜底完成时通知；不以历史成功、任务状态或 Successful 页脚代替校验。
