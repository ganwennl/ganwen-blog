# 第三方许可 / Third-party notices

本项目代码采用 [MIT License](LICENSE)，第三方组件保留各自原有版权与许可证。

Project code is licensed under the [MIT License](LICENSE). Third-party components retain their original copyright and license terms.

- Go 标准库 / Go standard library：随 Go 分发的 BSD 风格许可证。
- Go 模块依赖 / Go module dependencies：版本记录在 `go.mod` 与 `go.sum` 中，构建生成的发行包会在 `third-party-licenses/` 目录附带各依赖模块的许可证原文。
- IPv6 地址库解析参考 `itbdw/ip-database`，其 MIT 许可证保存在 `deploy/geoip-LICENSE-itbdw.txt` 并随发行包附带。 / IPv6 database parsing follows the format documented by `itbdw/ip-database` (`deploy/geoip-LICENSE-itbdw.txt`).
- 离线 IP 地址库数据文件不随安装包分发，可在管理后台按需下载更新。 / Offline IP database files are not bundled in release archives and can be updated from the admin panel.
