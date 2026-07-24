[CmdletBinding()]
param(
    [ValidateSet('amd64', 'arm64')]
    [string] $Architecture = 'amd64',
    [string] $Version = 'dev'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$distRoot = Join-Path $projectRoot 'dist'
$packageName = "EverythingShare-v$Version-windows-$Architecture"
$packageRoot = Join-Path $distRoot $packageName
$archivePath = Join-Path $distRoot "$packageName.zip"

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Docker Desktop is required to build the Windows package.'
}
docker info *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Desktop is not running.'
}

New-Item -ItemType Directory -Path $packageRoot -Force | Out-Null
$containerCommand = @(
    'run', '--rm',
    '-v', "${projectRoot}:/src",
    '-w', '/src',
    '-e', 'GOOS=windows',
    '-e', "GOARCH=$Architecture",
    '-e', 'CGO_ENABLED=0',
    'golang:1.26.5-alpine3.24',
    'go', 'build', '-trimpath',
    "-ldflags=-s -w -X main.version=$Version",
    '-o', "/src/dist/$packageName/EverythingShare.exe",
    '.'
)
& docker @containerCommand
if ($LASTEXITCODE -ne 0) {
    throw 'Windows binary build failed.'
}

Copy-Item -LiteralPath (Join-Path $projectRoot 'packaging\windows\Start EverythingShare.cmd') -Destination $packageRoot -Force
Copy-Item -LiteralPath (Join-Path $projectRoot 'docs\windows-quickstart.zh-CN.md') -Destination $packageRoot -Force
Copy-Item -LiteralPath (Join-Path $projectRoot 'docs\windows-quickstart.en.md') -Destination $packageRoot -Force
Copy-Item -LiteralPath (Join-Path $projectRoot 'LICENSE') -Destination $packageRoot -Force

if (Test-Path -LiteralPath $archivePath) {
    Remove-Item -LiteralPath $archivePath -Force
}
Compress-Archive -LiteralPath $packageRoot -DestinationPath $archivePath -CompressionLevel Optimal
$hash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash

Write-Host "Windows package: $archivePath"
Write-Host "SHA256: $hash"
