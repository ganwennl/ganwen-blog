# 第三方许可 / Third-party notices

我自己的代码采用 MIT。第三方组件仍保留各自的版权和许可证，根目录的 MIT 不替代它们的许可。

My own code is MIT licensed. Third-party components keep their copyright and license terms; the project license does not replace them.

- Go 标准库 / Go standard library: BSD-style license distributed with Go.
- Go 依赖版本在 `go.mod` 和 `go.sum` 中；程序包的 `third-party-licenses` 包含实际编译依赖的原许可文本。 / Dependency versions are recorded in `go.mod` and `go.sum`. Release packages include the original licenses for the modules used in each build.
- IPv6 地址库读取逻辑参考 `itbdw/ip-database`，原 MIT 许可保存在 `deploy/geoip-LICENSE-itbdw.txt`，并随程序包附带。 / The IPv6 database reader uses the format documented by `itbdw/ip-database`. Its MIT license is retained in `deploy/geoip-LICENSE-itbdw.txt` and included in release packages.
- 地址库数据需要用户在后台自行更新，不随安装包附带。 / IP database data can be updated in the admin interface and is not bundled with the program.
