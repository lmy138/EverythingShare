[CmdletBinding()]
param(
    [string] $DestinationRoot
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($DestinationRoot)) {
    $DestinationRoot = Join-Path $projectRoot 'backups'
}

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$destination = Join-Path $DestinationRoot "everythingshare-$stamp"
$remoteName = "backup-$stamp.db"
$remotePath = "/data/$remoteName"

New-Item -ItemType Directory -Path $destination -Force | Out-Null

try {
    docker compose --project-directory $projectRoot -f (Join-Path $projectRoot 'docker-compose.yml') exec -T gateway share-gateway backup $remotePath
    if ($LASTEXITCODE -ne 0) { throw 'Gateway database backup failed.' }

    docker compose --project-directory $projectRoot -f (Join-Path $projectRoot 'docker-compose.yml') cp "gateway:$remotePath" (Join-Path $destination 'share-gateway.db')
    if ($LASTEXITCODE -ne 0) { throw 'Unable to copy the database backup from the container.' }

    foreach ($relative in @('.env', 'secrets\htpasswd')) {
        $source = Join-Path $projectRoot $relative
        if (Test-Path -LiteralPath $source) {
            Copy-Item -LiteralPath $source -Destination $destination
        }
    }

    try {
        $account = [Security.Principal.WindowsIdentity]::GetCurrent().Name
        & icacls $destination /inheritance:r /grant:r "${account}:(OI)(CI)(F)" 'SYSTEM:(OI)(CI)(F)' 'BUILTIN\Administrators:(OI)(CI)(F)' *> $null
    }
    catch {
        Write-Warning 'Could not tighten backup ACLs. Protect the backup directory manually.'
    }

    Write-Host "Backup created: $destination"
    Write-Host 'Store a second encrypted copy on another disk or host.'
}
finally {
    docker compose --project-directory $projectRoot -f (Join-Path $projectRoot 'docker-compose.yml') exec -T gateway rm -f $remotePath 2>$null | Out-Null
}
