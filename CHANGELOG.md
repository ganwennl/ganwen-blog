# 更新日志 / Changelog

## 未发布 / Unreleased

- 侧栏支持自定义排序：默认沿用系统顺序，开启后可拖动组件或使用上下移动按钮调整位置。
- Sidebar ordering can now be customized by dragging components or using the move buttons; the default mode keeps the system order.
- 新增独立“侧栏设置”页面，可选择搜索、热门文章、统计、年月归档、分类、标签和广告；归档支持按年或月浏览文章。
- Added a dedicated sidebar settings page for search, popular articles, statistics, date archives, categories, tags and ads. Archives support browsing posts by year or month.
- 修复新增显示语言的访问统计无法完整导出和恢复的问题，保留全部 11 种语言的统计记录。
- Fixed analytics export and restore for all 11 supported languages, preserving records for the newer display languages.

## v1.0.2 — 2026-10-15

- 修复后台一直停在“正在打开编辑后台…”的问题。
- 界面语言资源直接包含在后台页面里，启动不再依赖额外的语言脚本请求。
- 保留已有文章、设置和登录状态，不需要重新安装或重置账号。

- Fixed the admin interface getting stuck on its opening screen.
- Included interface language resources directly in the admin page, removing the extra script request from startup.
- Existing articles, settings and sign-in sessions are preserved; no reinstall or account reset is needed.

## v1.0.1 — 2026-10-15

- 前台和后台补齐 11 种显示语言，菜单、按钮和操作提示都跟随选择。
- 当前语言排在语言列表第一位，后台记住这台浏览器的选择；阿拉伯语和乌尔都语使用从右到左排版。
- 调整了一些生硬的中文用语，修好英文按钮拼词和小屏幕下的界面遮挡。
- 界面翻译直接打包，不需要配置翻译服务。文章和已有译文保持原样。

- Completed the public and admin interface in 11 display languages, including menus, buttons and messages.
- The selected language comes first. The admin interface remembers the browser's choice and supports right-to-left layouts for Arabic and Urdu.
- Made Chinese labels more natural and fixed joined English labels and overlapping controls on small screens.
- Interface translations are built in and need no translation service. Existing articles and translations are unchanged.

## v1.0.0 — 2026-10-14

- 首次公开发布，提供独立运行的 Windows x64 和 Linux x64 程序包。
- 增加虚拟访问量开关；关闭后主页总数和热门文章显示真实浏览量。每日展示基数可在后台调整。
- 增加显示站点统计开关。新安装默认真实统计，已有配置保留原展示方式。
- 首次启动自动生成管理员账号，增加站点地址参数、安装说明和备份升级说明。

- First public release with standalone Windows x64 and Linux x64 packages.
- Added an explicit virtual view count switch and a configurable daily display target. Turning it off uses recorded counts for site totals and popular articles.
- Added a site statistics visibility switch. Fresh installations use real counts; existing settings keep their previous behavior.
- Added first-run administrator setup, a site URL option, and installation, backup and upgrade instructions.
