param(
    [string]$DataDirectory = (Join-Path $PSScriptRoot 'data'),
    [ValidateRange(1, 65535)][int]$Port = 8097,
    [string]$SiteUrl = ''
)
$ErrorActionPreference = 'Stop'
$binary = Join-Path $PSScriptRoot 'ganwen.exe'
$dataPath = [IO.Path]::GetFullPath($DataDirectory)
if (-not (Test-Path -LiteralPath (Join-Path $dataPath 'runtime.env'))) {
    & $binary init-admin --data $dataPath
    if ($LASTEXITCODE -ne 0) { throw 'Administrator initialization failed.' }
}
Write-Host "Blog: http://127.0.0.1:$Port/"
Write-Host "Admin: http://127.0.0.1:$Port/admin/"
Write-Host "Administrator details: $(Join-Path $dataPath 'admin-access.txt')"
$serverArgs = @('serve', '--data', $dataPath, '--listen', "127.0.0.1:$Port")
if ($SiteUrl) { $serverArgs += @('--site-url', $SiteUrl) }
& $binary @serverArgs
exit $LASTEXITCODE
