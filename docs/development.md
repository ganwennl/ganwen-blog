# 开发与目录说明

本地开发需要 Go 1.26+、Python 3 和 Node.js。Python 测试依赖列在 `tests/requirements.txt`，可在虚拟环境中执行 `python -m pip install -r tests/requirements.txt` 安装。

在项目根目录运行测试、构建与打包：

```powershell
.\scripts\test.ps1
go build -o dist/ganwen.exe ./cmd/ganwen
python scripts/packaging/build-release.py --output dist/releases/local
```

分项执行测试：

```powershell
go test ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
python -m unittest discover -s tests/scripts -p 'test_*.py'
Get-ChildItem tests/client/test-*.cjs | ForEach-Object { node $_.FullName; if ($LASTEXITCODE) { throw $_.Name } }
```

## 目录约定

- `cmd/ganwen`：程序与维护子命令入口；`internal/blog`：核心实现与同包 Go 测试。
- `internal/blog/web/admin` 与 `internal/blog/web/public`：通过 `embed` 打包进二进制的前后台静态资源与多语言词典。
- `scripts/packaging`：构建与打包脚本；`scripts/backup`：服务器备份脚本；`scripts/migration`：历史数据导入工具；`scripts/verify`：站点只读检查工具。
- `tests/scripts`：Python 离线单元测试；`tests/client`：Node.js 前端逻辑测试；`tests/package`：Windows 发行包隔离启动验证。
- `packaging/windows`、`packaging/linux`：发行包内的启动脚本；`deploy`：Nginx 与 systemd 配置示例，配套说明见 [deployment](deployment/)。
- `data`、`migration`、`dist`：本地运行数据、迁移材料与构建输出目录，不纳入版本控制，也不会被 `build-release.py` 打入公开压缩包。

日常安装、升级与备份说明见[使用说明](usage.md)。
