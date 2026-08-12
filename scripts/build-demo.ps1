[CmdletBinding()]
param([string] $Version = 'dev')

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$distRoot = Join-Path $projectRoot 'dist'
$payloadPath = Join-Path $projectRoot 'third_party\everything\Everything.exe.bin'
$archiveName = 'Everything-1.4.1.1032.x64.zip'
$archiveHash = '698DF475EC44E638F66F1B6A32D28FEA613CEC78D3B6310E6ABE53431EEB940C'
$payloadHash = 'F191F756996A14A11E5445FA7103D302EFD510CF2FBF920E6C0C8ED51D512E36'
$downloadRoot = Join-Path $env:TEMP 'EverythingShare-Demo-Build'
$archivePath = Join-Path $downloadRoot $archiveName
$extractRoot = Join-Path $downloadRoot 'extracted'

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Docker Desktop is required to build the demo binary.'
}
docker info *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Desktop is not running.'
}

New-Item -ItemType Directory -Path $downloadRoot, $extractRoot, $distRoot -Force | Out-Null
if (-not (Test-Path -LiteralPath $archivePath) -or
    (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash -ne $archiveHash) {
    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($curl) {
        & $curl.Source --fail --location --retry 3 --retry-all-errors --proto '=https' --tlsv1.2 `
            --output $archivePath "https://www.voidtools.com/$archiveName"
        if ($LASTEXITCODE -ne 0) { throw 'Unable to download the official Everything archive.' }
    }
    else {
        $downloaded = $false
        for ($attempt = 1; $attempt -le 3 -and -not $downloaded; $attempt++) {
            try {
                Invoke-WebRequest -UseBasicParsing -Uri "https://www.voidtools.com/$archiveName" -OutFile $archivePath
                $downloaded = $true
            }
            catch {
                if ($attempt -eq 3) { throw }
                Start-Sleep -Seconds (2 * $attempt)
            }
        }
    }
}
if ((Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash -ne $archiveHash) {
    throw 'The official Everything archive SHA256 does not match.'
}
Expand-Archive -LiteralPath $archivePath -DestinationPath $extractRoot -Force
$everythingExe = Join-Path $extractRoot 'Everything.exe'
if ((Get-FileHash -LiteralPath $everythingExe -Algorithm SHA256).Hash -ne $payloadHash) {
    throw 'The extracted Everything.exe SHA256 does not match.'
}
Copy-Item -LiteralPath $everythingExe -Destination $payloadPath -Force

try {
    $outputName = "EverythingShare-Demo-v$Version-windows-x64.exe"
    $dockerArgs = @(
        'run', '--rm',
        '-v', "${projectRoot}:/src",
        '-w', '/src',
        '-e', 'GOOS=windows',
        '-e', 'GOARCH=amd64',
        '-e', 'CGO_ENABLED=0',
        'golang:1.26.5-alpine3.24',
        'go', 'build', '-trimpath',
        "-ldflags=-s -w -X main.version=$Version -X main.edition=demo",
        '-o', "/src/dist/$outputName",
        '.'
    )
    & docker @dockerArgs
    if ($LASTEXITCODE -ne 0) {
        throw 'Demo binary build failed.'
    }
    $outputPath = Join-Path $distRoot $outputName
    Write-Host "Demo binary: $outputPath"
    Write-Host "SHA256: $((Get-FileHash -LiteralPath $outputPath -Algorithm SHA256).Hash)"
}
finally {
    [IO.File]::WriteAllText(
        $payloadPath,
        "PLACEHOLDER: release builds replace this file with the verified official Everything.exe payload.`n",
        [Text.UTF8Encoding]::new($false)
    )
}
