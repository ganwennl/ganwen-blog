# 甘文个人博客 / Ganwen Personal Blog

我的个人博客，用 Go 写的，支持中文和多语言内容、文章管理及媒体文件。默认使用 SQLite，也支持 MySQL，保留已有中文链接和内容标识。

开发入口在 `cmd/ganwen/`，Go 版本见 `go.mod`。按原有 README 准备独立的数据目录、数据库和站点配置，再启动本机预览：

```shell
go run ./cmd/ganwen serve --data data --listen 127.0.0.1:8097 --worker=false
```

文章、数据库、媒体和迁移资料需要自行准备。第三方依赖保留原许可证。

## English

My personal blog, written in Go, with Chinese and multilingual content, article management and media support. It uses SQLite by default and also supports MySQL, while keeping existing Chinese URLs and content identifiers.

Start in `cmd/ganwen/` and use the Go version in `go.mod`. Follow the existing README to prepare a separate data directory, database and site settings, then run the local preview with the command above.

Provide your own articles, database, media and migration data. Third-party dependencies retain their original licenses.
