# 更新日志 / Changelog

## v1.0.10 — 2026-11-27

- 修复新安装默认 `https://localhost` 站点下，前台站内链接未转为相对路径导致本地 HTTP 预览跳转失败的问题；配置自定义 `--site-url` 后自动更新本地生成的默认文章/页面 canonical 链接。
- 修复翻译预处理中复合中文数词（如 `1亿5000万`、`2万5000`、`2万零50`）展开错位及常见词组（如 `万能`、`万一`）误展开的问题。
- 回收站内的文章或页面禁止直接保存译文；补齐后台快捷操作与翻译队列状态的多语言提示，清理界面文案。
- 改进 `runtime.env` 解析（兼容 UTF-8 BOM、`export` 前缀与引号包裹值），并在首次生成管理员凭据中途出错时清理残留文件。
- 发布构建脚本仅打包用户与开发文档，清理部署示例及备份脚本中的硬编码环境常量，并在打包结束时自动清理临时目录。

- Fixed local preview navigation on fresh `https://localhost` installs by converting localhost links to relative paths, and updated auto-generated localhost canonicals when `--site-url` is configured.
- Fixed Chinese compound number expansion (such as `1亿5000万`, `2万5000`, `2万零50`) and prevented false matches on common idioms (`万能`, `万一`) during translation preprocessing.
- Blocked saving translations onto trashed posts, added missing localized admin messages for list actions and queue statuses, and cleaned up UI tooltips.
- Improved `runtime.env` parsing (UTF-8 BOM, `export` prefix, and quoted values) and cleaned up partial files if `init-admin` fails mid-write.
- Excluded server-specific deployment notes from public release ZIPs, parameterized backup script constants via environment variables, and ensured temporary staging directories are cleaned up.

## v1.0.9 — 2026-11-25

- 后台登录限流在 Cloudflare 反向代理下按真实访客 IP（`CF-Connecting-IP` / `CF-Connecting-IPv6`）计数，避免边缘节点轮换绕过限流或误伤同节点正常登录。
- 站点设置与侧栏设置改为加锁更新，避免并发保存互相覆盖；修正 `public_stats_enabled` 校验提示。
- 后台页头图片与文章特色图片增加 URL 校验，拒绝 `javascript:`、协议相对地址及异常路径。
- 修复独立页面取消“仅地址访问”后无法清空访问地址恢复为普通页面的问题，以及关闭虚拟浏览开关时禁用输入框阻碍保存的问题。
- 前台访问统计支持按年份归档（`/?m=YYYY`）；修复多轨及 64 位长度 MP4 上传校验、`<img loading>` 重复属性与带尾斜杠 `BaseURL` 的 canonical 拼接问题。
- 清理 Nginx 配置模板中过时的 TLSv1.1 / 3DES 与 `swf` 规则，并为 `/admin/` 增加显式反代前缀。

- Keyed admin login rate limiting behind Cloudflare on verified visitor IPs (`CF-Connecting-IP` / `CF-Connecting-IPv6`).
- Serialized site-settings and sidebar-settings updates under the store write lock and fixed `public_stats_enabled` validation text.
- Added URL validation for header and featured images to reject `javascript:`, protocol-relative, and malformed URLs.
- Fixed clearing `public_path` when converting an independent page back to an ordinary page, and stopped disabled daily-view inputs from blocking saves.
- Enabled analytics for yearly archives (`/?m=YYYY`); fixed multi-track / 64-bit MP4 box validation, duplicate `<img loading>` attributes, and trailing-slash `BaseURL` canonicals.
- Updated Nginx templates to TLS 1.2+, removed 3DES and `swf`, and added an explicit `/admin/` proxy location.

## v1.0.8 — 2026-11-25

- 页面移到文章后，原 `?page_id=` 链接自动 301 跳转到新文章地址；普通文章仍拒绝 `?page_id=`。
- 独立页面（设置了自定义访问地址或仅地址访问）禁止直接移到文章，避免丢失路径或意外公开列出。

- Redirected (`301`) old `?page_id=` URLs after moving a page to posts while keeping `?page_id=` rejected for other posts.
- Prevented moving custom-path or address-only standalone pages directly to posts.

## v1.0.7 — 2026-11-25

- 后台文章与页面列表支持一键公开、隐藏，以及在文章与普通页面之间互转。
- 新增两步删除：删除先移入回收站，支持恢复为草稿，仅在回收站内可彻底删除。
- 新建普通页面首次保存时增加确认提示，避免误把文章发成页面。

- Added one-click publish, hide, and post/page conversion actions in admin lists.
- Added two-step deletion via a recoverable trash view before permanent removal.
- Added a confirmation prompt when saving a new ordinary page.

## v1.0.6 — 2026-11-23

- 后台媒体列表缓存 1 分钟并在新上传后立即刷新，减少重复扫描上传目录。
- 侧栏统计异步刷新期间先行返回上一次缓存结果，避免页面请求等待数据库查询。

- Cached the admin media listing for one minute with immediate invalidation on upload.
- Served the previous cached sidebar statistics while refreshing counts in the background.

## v1.0.5 — 2026-11-23

- 后台登录令牌改为无状态签名校验，避免匿名请求占满挑战池影响正常登录。
- 登录限流按 IPv6 `/64` 聚合；限流表满载时新来源共享小配额而非直接拒绝。
- 访问统计单来源（IPv4 或 IPv6 `/64`）每日上限 1000 次，并为统计表补充 `(kind,date)` 索引。

- Switched admin login challenges to stateless signed tokens.
- Grouped IPv6 login rate limits by `/64` with a bounded overflow bucket.
- Capped analytics at 1,000 daily events per source and added a `(kind,date)` index.

## v1.0.4 — 2026-10-24

- 手动翻译返回实际新增排队、进行中和跳过数量；强制重译需确认后才覆盖已有译文。
- 改进姓名保护边界、中文数量单位展开，以及图片 `alt` 和链接 `title` 翻译。
- 长文拆分为小批次请求并在响应不完整时二分重试，保留取消与并发编辑保护。

- Reported exact queued, active, and skipped language counts on translation requests, requiring confirmation before overwriting existing translations.
- Improved author-name boundary matching, Chinese numeric unit expansion, and image `alt` / link `title` translation.
- Split long articles into smaller batches with recursive retry on truncated structured responses.

## v1.0.3 — 2026-10-24

- 统一编辑器、预览和前台页面的文本对齐表现，新增“清除全部格式”及单步撤销。
- 修正外部表情图片尺寸显示，普通正文图片保持原比例。
- 联系方式支持开启联系前确认提示及多语言文案；新增独立“侧栏设置”页面与组件拖拽排序，支持按年/月归档浏览。
- 修复全部 11 种显示语言下的访问统计导出与恢复。

- Unified alignment handling across editor, preview, and public pages, and added whole-article formatting cleanup with undo.
- Sized inline external emoji images to text height without affecting regular images.
- Added optional pre-contact notices, a dedicated sidebar settings page with custom ordering, and year/month date archives.
- Fixed analytics export and import across all 11 display languages.

## v1.0.2 — 2026-10-15

- 修复后台偶发停留在启动加载页的问题，将界面语言词条内联到后台页面。

- Fixed the admin page getting stuck on its loading screen by inlining UI language resources.

## v1.0.1 — 2026-10-15

- 前台与后台支持 11 种界面显示语言，自动记忆选择，阿拉伯语和乌尔都语支持 RTL 从右到左排版。
- 优化中英文按钮文案与小屏布局。

- Added 11 built-in UI display languages across public and admin pages, including RTL layout for Arabic and Urdu.
- Refined button labels and small-screen responsiveness.

## v1.0.0 — 2026-10-14

- 首次公开发布 Windows x64 与 Linux amd64 独立运行包。
- 支持文章、页面、媒体上传、多语言译文管理、访问统计及逻辑备份导出/恢复。

- Initial public release with standalone Windows x64 and Linux amd64 packages.
- Included posts, pages, media uploads, multilingual translation workflow, visitor analytics, and logical backup/restore commands.
