[CmdletBinding()]
param(
    [ValidateSet('Basic', 'Oidc')]
    [string] $AuthMode = 'Basic',
    [string] $EverythingHttpRoot = (Join-Path $env:APPDATA 'Everything\HTTP Server'),
    [switch] $Force,
    [switch] $SkipStart
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$envPath = Join-Path $projectRoot '.env'
$htpasswdPath = Join-Path $projectRoot 'secrets\htpasswd'

function Read-Value {
    param([string] $Prompt, [string] $Default)
    $suffix = if ($Default) { " [$Default]" } else { '' }
    $value = Read-Host "$Prompt$suffix"
    if ([string]::IsNullOrWhiteSpace($value)) { return $Default }
    return $value.Trim()
}

function Read-PlainSecret {
    param([string] $Prompt)
    $secure = Read-Host $Prompt -AsSecureString
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
    }
    finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
    }
}

function New-RandomBase64Url {
    param([int] $Bytes = 32)
    $buffer = New-Object byte[] $Bytes
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($buffer) } finally { $rng.Dispose() }
    return [Convert]::ToBase64String($buffer).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Format-DotEnvValue {
    param([AllowEmptyString()][string] $Value)
    if ($Value -match '[\r\n]') { throw 'Configuration values cannot contain newlines.' }
    # Compose treats single-quoted dotenv values literally. Escape only the
    # quote delimiter so provider secrets containing $, # or spaces survive.
    return "'" + $Value.Replace("'", "\'") + "'"
}

if ($env:OS -ne 'Windows_NT') {
    throw 'The guided setup script currently supports Windows hosts only.'
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Docker Desktop is required. Install it and run this script again.'
}
docker info *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Desktop is not running.'
}
if ((Test-Path -LiteralPath $envPath) -and -not $Force) {
    throw ".env already exists. Use -Force only if you intend to replace the current configuration."
}

Write-Host ''
Write-Host "EverythingShare guided setup ($AuthMode authentication)"
Write-Host 'No secret entered below is uploaded or written to the Git repository.'
Write-Host ''

$edgePort = Read-Value 'Local edge port' '8088'
$everythingHost = Read-Value 'Protected Everything host name' 'everything.localhost'
$shareHost = Read-Value 'Public share host name' 'share.localhost'
$defaultPublicUrl = if ($shareHost.EndsWith('.localhost')) {
    "http://${shareHost}:$edgePort"
} else {
    "https://$shareHost"
}
$publicBaseUrl = Read-Value 'Public share base URL' $defaultPublicUrl
$everythingBaseUrl = Read-Value 'Everything HTTP server URL as seen by Docker' 'http://host.docker.internal:8081'

$everythingUser = Read-Value 'Everything HTTP username' 'admin'
$everythingPassword = Read-PlainSecret 'Everything HTTP password'
if ([string]::IsNullOrEmpty($everythingPassword)) {
    throw 'Everything HTTP password cannot be empty.'
}
$everythingBasic = 'Basic ' + [Convert]::ToBase64String(
    [Text.Encoding]::UTF8.GetBytes("${everythingUser}:$everythingPassword")
)
$everythingPassword = $null

$adminSharedKey = New-RandomBase64Url 36
$sessionSecret = New-RandomBase64Url 32
$cookieSecure = $publicBaseUrl.StartsWith('https://', [StringComparison]::OrdinalIgnoreCase).ToString().ToLowerInvariant()

$oidcIssuer = 'https://identity.example.com/oidc'
$oidcClientId = 'REPLACE_WITH_OIDC_CLIENT_ID'
$oidcClientSecret = 'REPLACE_WITH_OIDC_CLIENT_SECRET'
$oidcCookieSecret = New-RandomBase64Url 32
$oidcRedirectUrl = "https://$everythingHost/oauth2/callback"

New-Item -ItemType Directory -Path (Split-Path -Parent $htpasswdPath) -Force | Out-Null
if ($AuthMode -eq 'Basic') {
    $adminUser = Read-Value 'EverythingShare login username' 'admin'
    if ($adminUser -notmatch '^[A-Za-z0-9._-]{1,64}$') {
        throw 'The login username may contain only letters, numbers, dot, underscore and dash.'
    }
    $adminPassword = Read-PlainSecret 'EverythingShare login password'
    if ($adminPassword.Length -lt 10) {
        throw 'Use an EverythingShare login password with at least 10 characters.'
    }
    $hashLine = $adminPassword | docker run --rm -i httpd:2.4-alpine htpasswd -niB $adminUser
    $adminPassword = $null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($hashLine)) {
        throw 'Unable to generate the protected password file.'
    }
    [IO.File]::WriteAllText($htpasswdPath, ($hashLine.Trim() + [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
}
else {
    [IO.File]::WriteAllText($htpasswdPath, "# OIDC mode does not use this file.`n", [Text.UTF8Encoding]::new($false))
    $oidcIssuer = Read-Value 'OIDC issuer URL' ''
    $oidcClientId = Read-Value 'OIDC client ID' ''
    $oidcClientSecret = Read-PlainSecret 'OIDC client secret'
    $oidcRedirectUrl = Read-Value 'OIDC redirect URL' $oidcRedirectUrl
    if (-not $publicBaseUrl.StartsWith('https://') -or -not $oidcRedirectUrl.StartsWith('https://')) {
        throw 'OIDC mode requires HTTPS public and redirect URLs.'
    }
}

$envLines = @(
    "EVERYTHING_HOST=$(Format-DotEnvValue $everythingHost)"
    "SHARE_HOST=$(Format-DotEnvValue $shareHost)"
    "EDGE_BIND=$(Format-DotEnvValue '127.0.0.1')"
    "EDGE_PORT=$(Format-DotEnvValue $edgePort)"
    "PUBLIC_BASE_URL=$(Format-DotEnvValue $publicBaseUrl)"
    "COOKIE_SECURE=$(Format-DotEnvValue $cookieSecure)"
    "EVERYTHING_BASE_URL=$(Format-DotEnvValue $everythingBaseUrl)"
    "EVERYTHING_AUTH_HEADER=$(Format-DotEnvValue $everythingBasic)"
    "SESSION_SECRET=$(Format-DotEnvValue $sessionSecret)"
    "ADMIN_SHARED_KEY=$(Format-DotEnvValue $adminSharedKey)"
    "TRUST_PROXY_HEADERS=$(Format-DotEnvValue 'true')"
    "TZ=$(Format-DotEnvValue 'Asia/Shanghai')"
    "ZIP_CACHE_THRESHOLD_BYTES=$(Format-DotEnvValue '5368709120')"
    "ZIP_CACHE_MAX_BYTES=$(Format-DotEnvValue '53687091200')"
    "ZIP_CACHE_TTL_HOURS=$(Format-DotEnvValue '24')"
    "ZIP_CACHE_MIN_FREE_BYTES=$(Format-DotEnvValue '21474836480')"
    "OIDC_ISSUER_URL=$(Format-DotEnvValue $oidcIssuer)"
    "OIDC_CLIENT_ID=$(Format-DotEnvValue $oidcClientId)"
    "OIDC_CLIENT_SECRET=$(Format-DotEnvValue $oidcClientSecret)"
    "OIDC_COOKIE_SECRET=$(Format-DotEnvValue $oidcCookieSecret)"
    "OIDC_REDIRECT_URL=$(Format-DotEnvValue $oidcRedirectUrl)"
)
[IO.File]::WriteAllLines($envPath, $envLines, [Text.UTF8Encoding]::new($false))

try {
    $account = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    & icacls $envPath /inheritance:r /grant:r "${account}:(F)" 'SYSTEM:(F)' 'BUILTIN\Administrators:(F)' *> $null
    & icacls $htpasswdPath /inheritance:r /grant:r "${account}:(F)" 'SYSTEM:(F)' 'BUILTIN\Administrators:(F)' *> $null
}
catch {
    Write-Warning 'Could not tighten the configuration file ACL. Protect .env and secrets\htpasswd manually.'
}

& (Join-Path $PSScriptRoot 'install-ui.ps1') -EverythingHttpRoot $EverythingHttpRoot -Force

$composeArgs = @('compose', '--project-directory', $projectRoot, '-f', (Join-Path $projectRoot 'docker-compose.yml'))
if ($AuthMode -eq 'Oidc') {
    $composeArgs += @('-f', (Join-Path $projectRoot 'docker-compose.oidc.yml'))
}

& docker @composeArgs config --quiet
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Compose configuration validation failed.'
}

if (-not $SkipStart) {
    & docker @composeArgs up -d --build
    if ($LASTEXITCODE -ne 0) {
        throw 'Docker deployment failed. Run scripts\check.ps1 for details.'
    }

    $healthy = $false
    for ($attempt = 1; $attempt -le 60; $attempt++) {
        try {
            $result = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$edgePort/healthz" -Headers @{ Host = $shareHost } -TimeoutSec 3
            if ($result.StatusCode -eq 200) {
                $healthy = $true
                break
            }
        }
        catch {}
        Start-Sleep -Seconds 2
    }
    if (-not $healthy) {
        throw 'EverythingShare did not become healthy within two minutes. Run scripts\check.ps1.'
    }
}

Write-Host ''
Write-Host 'EverythingShare setup is complete.'
Write-Host "Protected search: http://${everythingHost}:$edgePort/"
Write-Host "Share management: http://${everythingHost}:$edgePort/share-admin/"
Write-Host "Public share endpoint: $publicBaseUrl"
if ($AuthMode -eq 'Oidc') {
    Write-Host 'OIDC mode: configure your HTTPS reverse proxy before testing sign-in.'
}
