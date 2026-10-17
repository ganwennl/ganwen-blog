# 开发与目录约定

需要 Go 1.26 或更新版本、Python 3 和 Node.js。Python 测试依赖见 `tests/requirements.txt`，可用 `python -m pip install -r tests/requirements.txt` 安装到自己的虚拟环境；`scripts/test.ps1 -Python <解释器路径>` 可选择已有环境。以下命令在项目根目录运行：

```powershell
.\scripts\test.ps1
go build -o dist/ganwen.exe ./cmd/ganwen
python scripts/packaging/build-release.py --output dist/releases/local
```

分项检查：

```powershell
go test ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
python -m unittest discover -s tests/scripts -p 'test_*.py'
Get-ChildItem tests/client/test-*.cjs | ForEach-Object { node $_.FullName; if ($LASTEXITCODE) { throw $_.Name } }
```

- Go 测试与实现同包放置；嵌入前端文件在 `internal/blog/web/admin` 和 `web/public`，HTTP 地址保持不变。
- `scripts/packaging` 负责打包与更新；`scripts/backup` 负责备份；`scripts/migration` 处理导入；`scripts/verify` 做线上检查。生产脚本不属于普通测试入口，运行前须明确目标和参数。
- `tests/scripts` 是不接触真实生产数据的 Python 回归；`tests/client` 是 Node.js 前端回归。
- `tests/package/test_release_package.py --package <Windows包> --output <新的临时目录>` 使用隔离数据验证成品启动。
- `packaging/windows`、`packaging/linux` 是发布包的启动脚本模板；`deploy` 只放配置模板，部署说明在 [deployment](deployment/)。
- `data`、`migration`、`dist` 是被 Git 忽略的本地目录，数据库、账号、媒体和恢复材料保留原位置。

构建发布包不会复制本地数据或真实配置。程序使用、升级和恢复见[使用说明](usage.md)。
