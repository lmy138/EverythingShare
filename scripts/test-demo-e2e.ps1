[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $BinaryPath
)

$ErrorActionPreference = 'Stop'
$binary = (Resolve-Path -LiteralPath $BinaryPath).Path
$demoRoot = Join-Path $env:LOCALAPPDATA 'EverythingShare Demo'
$statePath = Join-Path $demoRoot 'demo-state.json'
$downloadPath = Join-Path $env:TEMP 'EverythingShare-Demo-e2e-download.txt'
$stdoutPath = Join-Path $env:TEMP 'EverythingShare-Demo-e2e.stdout.log'
$stderrPath = Join-Path $env:TEMP 'EverythingShare-Demo-e2e.stderr.log'
$launcher = $null

function ConvertTo-Utf8JsonBytes([object] $Value) {
    return [Text.Encoding]::UTF8.GetBytes(($Value | ConvertTo-Json -Compress))
}

try {
    Remove-Item -LiteralPath $downloadPath,$stdoutPath,$stderrPath -Force -ErrorAction SilentlyContinue
    $launcher = Start-Process -FilePath $binary -ArgumentList '--no-browser' -PassThru `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath

    $deadline = [DateTime]::UtcNow.AddSeconds(35)
    do {
        try {
            $health = Invoke-RestMethod -UseBasicParsing -Uri 'http://127.0.0.1:18088/healthz' -TimeoutSec 2
            break
        }
        catch {
            if ($launcher.HasExited) {
                $details = Get-Content -LiteralPath $stderrPath -Raw -ErrorAction SilentlyContinue
                throw "Demo launcher exited early with code $($launcher.ExitCode): $details"
            }
            Start-Sleep -Milliseconds 300
        }
    } while ([DateTime]::UtcNow -lt $deadline)
    if (-not $health) { throw 'Demo HTTP endpoint did not become ready.' }

    $root = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18088/' -TimeoutSec 10
    if ($root.StatusCode -ne 200 -or $root.Content -notmatch 'EverythingShare') {
        throw 'The embedded search UI did not load.'
    }

    $sourcePath = Join-Path $demoRoot 'Demo Files\欢迎使用 EverythingShare.txt'
    $createBody = ConvertTo-Utf8JsonBytes @{
        sourcePath = $sourcePath
        type = 'file'
        title = 'EverythingShare 一键 Demo'
        code = 'DEMO88'
    }
    $share = Invoke-RestMethod -UseBasicParsing -Method Post -Uri 'http://127.0.0.1:18088/share-api/v1/shares' `
        -ContentType 'application/json; charset=utf-8' -Body $createBody -TimeoutSec 15
    if (-not $share.baseUrl -or $share.url -notmatch '#code=DEMO88') {
        throw 'The generated share URL does not include the extraction code.'
    }

    $token = ($share.baseUrl -split '/')[-1]
    $guest = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $verifyBody = ConvertTo-Utf8JsonBytes @{ code = 'DEMO88' }
    $verified = Invoke-RestMethod -UseBasicParsing -Method Post -Uri "http://127.0.0.1:18088/api/v1/public/shares/$token/verify" `
        -ContentType 'application/json; charset=utf-8' -Body $verifyBody -WebSession $guest -TimeoutSec 15
    if (-not $verified.verified) { throw 'Extraction-code verification failed.' }

    $ticket = Invoke-RestMethod -UseBasicParsing -Method Post -Uri "http://127.0.0.1:18088/api/v1/public/shares/$token/downloads" `
        -ContentType 'application/json; charset=utf-8' -Body (ConvertTo-Utf8JsonBytes @{}) -WebSession $guest -TimeoutSec 15
    Invoke-WebRequest -UseBasicParsing -Uri $ticket.url -WebSession $guest -OutFile $downloadPath -TimeoutSec 15
    $downloaded = Get-Content -LiteralPath $downloadPath -Raw -Encoding utf8
    if ($downloaded -notmatch 'EverythingShare 一键 Demo') {
        throw 'The downloaded demo file content does not match.'
    }

    $listed = Invoke-RestMethod -UseBasicParsing -Uri 'http://127.0.0.1:18088/share-api/v1/shares' -TimeoutSec 15
    if (-not ($listed.shares | Where-Object { $_.id -eq $share.id })) {
        throw 'The new share is missing from share management.'
    }

    Write-Host 'PASS: embedded search, share creation, extraction code, download, and share management.'
}
finally {
    if ($launcher -and -not $launcher.HasExited) {
        Stop-Process -Id $launcher.Id -Force -ErrorAction SilentlyContinue
        $launcher.WaitForExit()
    }
    if (Test-Path -LiteralPath $statePath) {
        $state = Get-Content -LiteralPath $statePath -Raw -Encoding utf8 | ConvertFrom-Json
        $everythingExe = Join-Path $demoRoot 'Everything\Everything.exe'
        if (Test-Path -LiteralPath $everythingExe) {
            & $everythingExe -instance $state.instance_name -exit
        }
    }
    Remove-Item -LiteralPath $downloadPath -Force -ErrorAction SilentlyContinue
}
