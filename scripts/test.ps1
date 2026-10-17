param([string]$Python = 'python')
$ErrorActionPreference = 'Stop'
$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$repo = Split-Path -Parent $PSScriptRoot
Push-Location -LiteralPath $repo
try {
    go test ./cmd/... ./internal/...
    if ($LASTEXITCODE -ne 0) { throw 'Go tests failed.' }
    go vet ./cmd/... ./internal/...
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed.' }
    & $Python -B -X utf8 -m unittest discover -s tests/scripts -p 'test_*.py'
    if ($LASTEXITCODE -ne 0) { throw 'Python tests failed.' }
    foreach ($test in Get-ChildItem -LiteralPath (Join-Path $repo 'tests/client') -Filter 'test-*.cjs' -File | Sort-Object Name) {
        node $test.FullName
        if ($LASTEXITCODE -ne 0) { throw "Client test failed: $($test.Name)" }
    }
} finally { Pop-Location }
