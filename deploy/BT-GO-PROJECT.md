# 宝塔原生 Go 项目

ganwen.nl 已于 2026-10-07 通过宝塔「网站 → Go项目」部署。后续启动、停止、重启、日志、域名和 SSL 均在此项目管理。服务器无需安装 Go SDK。

## 当前配置

| 项目 | 配置 |
| --- | --- |
| 项目名称 | `ganwen_blog` |
| 备注 | 甘文四语博客 |
| 执行文件 | `/www/wwwroot/ganwen-go/ganwen` |
| 项目端口 | `8099` |
| 运行用户 | `www` |
| 开机启动 | 已开启，宝塔自带进程守护 |
| 运行环境 | 无，程序自动读取私密 `data/runtime.env` |
| 外网映射 | 已开启 |
| 域名 | `ganwen.nl`、`www.ganwen.nl` |
| SSL | 已部署证书夹中的 `*.ganwen.nl ganwen.nl`，到期日 2026-12-01 |
| 数据库 | 本机 SQLite：`/www/wwwroot/ganwen-go/data/blog.db` |
| Nginx 配置 | `/www/server/panel/vhost/nginx/go_ganwen_blog.conf` |

启动命令：

```text
/www/wwwroot/ganwen-go/ganwen serve --data /www/wwwroot/ganwen-go/data --media /www/wwwroot/ganwen-go/data/media --listen 127.0.0.1:8099 --preview=false
```

端口只监听本机，不勾选「放行端口」。证书适用于域名，迁移 PHP 到 Go 无须重新申请；本次直接部署现有有效证书。通配证书不代表将所有子域名绑定博客，其他现有子站继续使用各自项目。

旧 PHP 项目 `ganwen.nl` 已停止，域名占位为 `wordpress-archive.ganwen.invalid`；没有为占位域名添加 DNS。旧网站文件、WordPress 数据库继续保留。临时 systemd 服务 `ganwen-blog` 已禁用和停止，线上只由宝塔 Go 项目运行。

## 在宝塔设置项目

1. 在「文件」将部署包解压到 `/www/wwwroot`，得到独立 `ganwen-go/`，保留原站目录。现有安装已完成，不要重复解压覆盖数据。
2. 在「网站 → Go项目 → 添加Go项目」填入上表路径、端口、运行用户和启动命令，勾选开机启动，环境选「无」。
3. 在域名管理绑定 `ganwen.nl` 和 `www.ganwen.nl`。同一域名不能同时绑定 PHP 和 Go 项目；迁移时先给旧项目加占位域名，再将真实域名移到 Go 项目。
4. 开启「外网映射」，在「SSL → 证书夹」选择有效证书，仅部署到 `ganwen_blog`。以后也可在此申请或更新证书。
5. 在 Go 项目「配置文件」粘贴 [ganwen-go.bt.conf.example](ganwen-go.bt.conf.example) 的完整内容并保存。这是此次线上实际使用的配置，保留旧图片和独立证据页，博客、后台和新上传交给 Go。
6. 旧 `/?p=ID`、`/?page_id=ID`、`/index.php?p=ID` 和查询参数由 Go 兼容。无需设置 WordPress PHP 伪静态；不要把旧 PHP rewrite 加入 Go 项目。

后台：[https://www.ganwen.nl/admin/](https://www.ganwen.nl/admin/)。在宝塔文件中私密查看 `/www/wwwroot/ganwen-go/data/admin-access.txt` 领取已有管理员账号密码。整个 `data/` 不公开映射。后台图片上传、头像、联系信息及四语编辑已具备，分类/标签可直接复选；后续自动翻译在「翻译」选择 OpenAI、DeepSeek 或兼容服务，再配置自己的 API 地址、模型和 Key，现有译文可直接使用。

前台支持浅色、深色和跟随系统，后台右上角直接切换白色／暗黑；主题会记住。联系地址自动识别 Gmail 等邮箱及社交媒体图标，普通网站读取自身图标，允许多个邮箱和自定义名称。连续修改原文会等待 30 秒合并为最新三语翻译；手动翻译立即执行，后台按文章和语言显示当前任务，旧历史保留。新文章编号从 10000 开始，旧 WordPress 编号和地址不变。

## 2026-10-08 阅读主题与编辑修复

本轮程序包含可选日志/经典主题和后台默认版式、SEO 元数据、管理员持久会话、前台列表原位编辑、设置页脚与十语翻译，以及 RSS 停用。新版本使用既有 KV 存储，不增加数据表，回退旧程序不需删除数据库结构。首次从旧内存会话升级后需重新登录一次；之后服务重启会保留有效登录。

更新通过宝塔 Go 项目控制器停止/启动 `ganwen_blog`，仅替换程序；发布前在线一致快照、停机边界快照及旧程序均保存到独立私密备份目录。核对程序 SHA-256、文章/翻译配置不变及 HTTP 健康，失败自动恢复旧程序。外部翻译维持现有手动模式，发布不触发翻译请求。

本轮最终程序 SHA-256 为 `cc0d0601dbf397c4e572b2de25fe222213521902455c5aba70dcf6d8e93095d4`。最近一次替换前快照位于 `/www/backup/ganwen-default-layout-20261008T020000Z/`；本轮全部界面/登录修改前程序与数据快照位于 `/www/backup/ganwen-admin-footer-20261008T015245Z/`。各目录保存 `ganwen.before`、`before.sqlite` 和部署结果，仅私密使用。

## 后续更新程序

1. 先用下节的方法备份当前数据；新安装包只用于新安装或完整恢复，不要覆盖正在使用的 `data/`。
2. 在宝塔「文件」上传新 Linux amd64 程序到 `/www/wwwroot/ganwen-go/ganwen.next`，核对发布文件的 SHA-256。服务器无需编译。
3. 在「网站 → Go项目 → ganwen_blog → 服务状态」停止项目；将当前 `ganwen` 复制到私密备份目录，再将 `ganwen.next` 重命名为 `ganwen`，设置执行权限 755。
4. 在同一「服务状态」启动项目，确认运行中，再访问首页、旧文章地址和后台。更新只更换程序文件，不重置文章、译文、媒体、账号或翻译服务设置。
5. 若程序更新异常，在宝塔停止项目，将保存的旧程序恢复到原路径，再启动。数据库另有变化时，应先保留最新数据并按完整备份恢复步骤处理。

## 验收与备份

最终公网验收已通过：140 个原公开语言页面、36 个草稿/私密语言地址、64 个分类/标签归档、212 个原媒体地址及新增文章的 4 个语言页面；旧链接、canonical/hreflang、RSS/sitemap、语言选择和匿名后台隔离均通过正常 HTTPS 证书校验。线上后台登录、文章清单、编辑器和三语当前翻译队列已验证。源站 `/wp-config.php`、`/data/runtime.env` 返回 404；公网 Cloudflare 对 `/wp-config.php` 主动返回 403 阻断页。

本机可重新执行只读公网验收：

```powershell
Set-Location 'D:\newcode\ganwenblog'
python -X utf8 scripts/verify-production.py --source migration/wordpress.json --media-manifest migration/media-manifest.json
```

站点运营后，旧 WordPress 快照可能包含后来公开的草稿，也可能仍将已改源文的旧译文视为公开。此时可添加 `--publication-manifest <部署前发布清单.json>`，格式为 `{文章ID: [当前公开语言代码]}`；清单必须由受控数据库快照按原文哈希和译文发布状态生成。验收会检查待审核语言为 404、缺失于 sitemap，并继续保护未显式公开的历史私密内容；不能从待验收网页反推期望状态。

原站切换前快照：`/www/backup/ganwen-migration-20261007T193335Z/`。包含原 Nginx 配置、WordPress 文件压缩包、数据库 SQL 压缩包和独立证据文件指纹，压缩包已验证可读，原 212 个媒体文件指纹一致。

最终线上一致导出为该目录下的 `ganwen-online-final.json` 及私密 `.translation.key` 侧文件，包含上线后新增的 #10000；`dist/ganwen-blog-bt-20261007-final.tar.gz` 从此导出构建，程序与线上相同。旧程序分别保存在同目录 `ganwen-before-ui` 和 `ganwen-before-contact-queue`，便于回退。完整包含账号和翻译服务配置，应私密保存。

自动备份通过宝塔全部网站 / 原 ALL MySQL 和项目补充任务共同覆盖，网盘保留 3650 份，新博客沿用原 `ganwen.nl` 网盘目录。旧网站任务暂停保留作配置模板，新任务复用原生备份并校验上传。补充任务对 SQLite 做在线一致快照，独立导出并恢复核对 Ganwen，保存媒体、程序和配置，加密后上传私有 Google Drive；详情和恢复方法见 [BT-AUTO-BACKUP.md](BT-AUTO-BACKUP.md)。不要单独复制运行中的 `blog.db` 而遗漏 WAL。

## 恢复旧 WordPress

1. 先备份 Go 当前完整数据；停止 Go 项目并从其域名管理移除两个真实域名，保留项目和文件。
2. 在旧 PHP 项目重新绑定 `ganwen.nl`、`www.ganwen.nl`，运行版本恢复 PHP 8.2，再启动旧项目。
3. 通过宝塔「文件」打开上述快照的 `ganwen.nl.conf`，将原完整内容恢复到旧 PHP 项目「配置文件」并保存；原 WordPress rewrite 文件和证书文件仍保留。
4. 在宝塔终端执行 `/www/server/nginx/sbin/nginx -t`，成功后重载 Nginx，核对原中文文章和 `/bewijs.html`。旧数据库和文件无须重新导入。

Go 上线后新增的文章、译文和媒体应先保留，恢复 WordPress 不会自动同步这些新增内容。
