# Windows 本地预构建脚本
# 用法:
#   powershell -NoProfile -File deploy/build-local.ps1

$ErrorActionPreference = 'Stop'
# PowerShell 7:关掉 "native 命令 stderr 自动触发终结" 的坑
if ($PSVersionTable.PSVersion.Major -ge 7) {
    $PSNativeCommandUseErrorActionPreference = $false
}

$root = Resolve-Path "$PSScriptRoot/.."
Set-Location $root

Write-Host "[build-local] repo  = $root"
Write-Host "[build-local] step1 = cross-build gpt2api"
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
New-Item -ItemType Directory -Force deploy/bin | Out-Null
go build -ldflags "-s -w" -o deploy/bin/gpt2api ./cmd/server
if ($LASTEXITCODE -ne 0) { throw "gpt2api build failed" }

Write-Host "[build-local] step2 = npm run build (web)"
Push-Location (Join-Path $root "web")
try {
    if (-not (Test-Path node_modules)) {
        npm install --no-audit --no-fund --loglevel=error
        if ($LASTEXITCODE -ne 0) { throw "npm install failed" }
    }
    npm run build
    if ($LASTEXITCODE -ne 0) { throw "npm run build failed" }
} finally {
    Pop-Location
}

Write-Host "[build-local] done. artifacts:"
Get-Item deploy/bin/gpt2api, web/dist/index.html | Format-Table -AutoSize
