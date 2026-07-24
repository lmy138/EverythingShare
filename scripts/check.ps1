[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$envPath = Join-Path $projectRoot '.env'

if (-not (Test-Path -LiteralPath $envPath)) {
    throw 'Missing .env. Run scripts\setup.ps1 first.'
}

$settings = @{}
foreach ($line in [IO.File]::ReadAllLines($envPath)) {
    if ($line -match '^([^#=]+)=(.*)$') {
        $settings[$matches[1]] = $matches[2]
    }
}

docker compose --project-directory $projectRoot -f (Join-Path $projectRoot 'docker-compose.yml') ps
docker compose --project-directory $projectRoot -f (Join-Path $projectRoot 'docker-compose.yml') logs --tail 50 gateway edge

$healthUri = "http://127.0.0.1:$($settings.EDGE_PORT)/healthz"
$response = Invoke-WebRequest -UseBasicParsing -Uri $healthUri -Headers @{ Host = $settings.SHARE_HOST } -TimeoutSec 10
if ($response.StatusCode -ne 200) {
    throw "Health check failed with HTTP $($response.StatusCode)."
}
Write-Host "Health check passed: $healthUri (Host: $($settings.SHARE_HOST))"
