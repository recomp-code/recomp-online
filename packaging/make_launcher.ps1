# The Recomp launcher for Windows and its update manifest (updater.go): <Out>\Recomp.exe and <Out>\recomp-launcher.json.
# Upload both to a recomp-online release; every installed launcher updates itself to it.
#   .\make_launcher.ps1 [-Out <dir>] [-Server https://...] [-Go <go.exe>]
param(
    [string]$Out = "$PSScriptRoot\..\dist",
    [string]$Server = 'https://nbas3.xellrecomp.party',
    [string]$Go = 'go'
)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force $Out | Out-Null
$Out = (Resolve-Path $Out).Path
$src = Join-Path $PSScriptRoot '..\launcher'
$version = (Select-String (Join-Path $src 'app.go') -Pattern 'const launcherVersion = "([^"]+)"').Matches[0].Groups[1].Value
Push-Location $src
try {
    & $Go build -tags desktop,production -trimpath -ldflags "-H windowsgui -s -w -X main.defaultServer=$Server" -o "$Out\Recomp.exe" .
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
} finally { Pop-Location }
$exe = Get-Item "$Out\Recomp.exe"
$manifest = [ordered]@{ version = $version
    files = [ordered]@{ Windows = [ordered]@{ file = 'Recomp.exe'; sha256 = (Get-FileHash $exe.FullName -Algorithm SHA256).Hash.ToLower(); size = $exe.Length } } }
[IO.File]::WriteAllText("$Out\recomp-launcher.json", ($manifest | ConvertTo-Json -Depth 5), (New-Object Text.UTF8Encoding($false)))
"Recomp $version, {0:N1} MB" -f ($exe.Length / 1MB)
