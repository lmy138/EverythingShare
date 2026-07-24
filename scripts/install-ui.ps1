[CmdletBinding()]
param(
    [string] $EverythingHttpRoot = (Join-Path $env:APPDATA 'Everything\HTTP Server'),
    [switch] $Force
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$sourceRoot = Join-Path $projectRoot 'everything-ui'

if (-not (Test-Path -LiteralPath $sourceRoot)) {
    throw "UI source directory not found: $sourceRoot"
}

if (-not (Test-Path -LiteralPath $EverythingHttpRoot)) {
    New-Item -ItemType Directory -Path $EverythingHttpRoot -Force | Out-Null
}

$files = @('main.css', 'share-ui.js')
$existing = $files | Where-Object { Test-Path -LiteralPath (Join-Path $EverythingHttpRoot $_) }
if ($existing.Count -gt 0) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $backupRoot = Join-Path $projectRoot "backups\everything-http-$stamp"
    New-Item -ItemType Directory -Path $backupRoot -Force | Out-Null
    foreach ($name in $existing) {
        Copy-Item -LiteralPath (Join-Path $EverythingHttpRoot $name) -Destination $backupRoot
    }
    Write-Host "Existing Everything HTTP assets backed up to $backupRoot"
}

foreach ($name in $files) {
    Copy-Item -LiteralPath (Join-Path $sourceRoot $name) -Destination (Join-Path $EverythingHttpRoot $name) -Force
}

Write-Host "EverythingShare UI installed in $EverythingHttpRoot"
Write-Host 'Restart Everything, or disable and re-enable its HTTP server if the old UI remains cached.'
