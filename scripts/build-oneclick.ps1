[CmdletBinding()]
param(
    [string]$OutputName = 'EverythingShare-OneClick-windows-x64.exe'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$distRoot = Join-Path $projectRoot 'dist'
$payloadPath = Join-Path $projectRoot 'third_party\everything\Everything.exe.bin'
$localEverythingPath = Join-Path $distRoot 'Everything\Everything.exe'
if ([IO.Path]::GetFileName($OutputName) -ne $OutputName -or -not $OutputName.EndsWith('.exe', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'OutputName must be a Windows executable filename without a directory.'
}
$outputPath = Join-Path $distRoot $OutputName
$archiveHash = '698DF475EC44E638F66F1B6A32D28FEA613CEC78D3B6310E6ABE53431EEB940C'
$executableHash = 'F191F756996A14A11E5445FA7103D302EFD510CF2FBF920E6C0C8ED51D512E36'
$downloadURL = 'https://www.voidtools.com/Everything-1.4.1.1032.x64.zip'
$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ("EverythingShare-OneClick-build-" + [Guid]::NewGuid().ToString('N'))
$originalPayload = [IO.File]::ReadAllBytes($payloadPath)

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Docker Desktop is required to build the Windows package.'
}
docker info *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Desktop is not running.'
}

New-Item -ItemType Directory -Path $temporaryRoot -Force | Out-Null
New-Item -ItemType Directory -Path $distRoot -Force | Out-Null
$archivePath = Join-Path $temporaryRoot 'Everything.zip'
$extractPath = Join-Path $temporaryRoot 'extracted'

try {
    if ((Test-Path -LiteralPath $localEverythingPath -PathType Leaf) -and
        (Get-FileHash -LiteralPath $localEverythingPath -Algorithm SHA256).Hash -eq $executableHash) {
        Write-Host 'Using the locally installed, SHA256-verified official Everything.exe...'
        $everythingExe = $localEverythingPath
    } else {
        Write-Host 'No verified local Everything.exe was found; downloading the official portable archive...'
        Invoke-WebRequest -UseBasicParsing -Uri $downloadURL -OutFile $archivePath
        if ((Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash -ne $archiveHash) {
            throw 'The official Everything archive SHA256 does not match.'
        }
        Expand-Archive -LiteralPath $archivePath -DestinationPath $extractPath
        $everythingExe = Join-Path $extractPath 'Everything.exe'
        if ((Get-FileHash -LiteralPath $everythingExe -Algorithm SHA256).Hash -ne $executableHash) {
            throw 'The extracted Everything.exe SHA256 does not match.'
        }
    }
    Copy-Item -LiteralPath $everythingExe -Destination $payloadPath -Force

    $dockerArgs = @(
        'run', '--rm',
        '-v', "${projectRoot}:/src",
        '-w', '/src',
        'golang:1.26.6-alpine3.24',
        'sh', '-c',
        "gofmt -w bundled_everything_windows.go bundled_everything_stub.go standalone.go && go test ./... && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -tags bundled_everything -o /tmp/everythingshare-tests.exe . && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags bundled_everything -trimpath -ldflags='-s -w -X main.version=one-click-package' -o '/src/dist/$OutputName' ."
    )
    & docker @dockerArgs
    if ($LASTEXITCODE -ne 0) {
        throw 'Windows one-click binary build failed.'
    }
} finally {
    [IO.File]::WriteAllBytes($payloadPath, $originalPayload)
    if (Test-Path -LiteralPath $temporaryRoot) {
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}

$hash = (Get-FileHash -LiteralPath $outputPath -Algorithm SHA256).Hash
Write-Host "One-click binary: $outputPath"
Write-Host "SHA256: $hash"
