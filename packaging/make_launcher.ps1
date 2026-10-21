# The Recomp launcher for Windows and its update manifest (updater.go): <Out>\Recomp.exe and <Out>\recomp-launcher.json.
# Upload both to a recomp-online release; every installed launcher updates itself to it. The other systems' entries
# (macOS) are kept: from -Merge <a recomp-launcher.json>, or else from the latest release's.
#   .\make_launcher.ps1 [-Out <dir>] [-Server https://...] [-Go <go.exe>] [-Merge <file>]
param(
    [string]$Out = "$PSScriptRoot\..\dist",
    [string]$Server = 'https://nbas3.xellrecomp.party',
    [string]$Go = 'go',
    [string]$Merge = ''
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
$files = [ordered]@{}
$prev = $null
if ($Merge) { $prev = Get-Content $Merge -Raw | ConvertFrom-Json }
else {
    try {
        $rel = Invoke-RestMethod 'https://api.github.com/repos/recomp-code/recomp-online/releases/latest' -Headers @{ 'User-Agent' = 'make_launcher' }
        $u = ($rel.assets | Where-Object name -eq 'recomp-launcher.json').browser_download_url
        if ($u) { $prev = Invoke-RestMethod $u -Headers @{ 'User-Agent' = 'make_launcher' } }
    } catch { Write-Warning "could not read the published manifest: $_" }
}
if ($prev) { foreach ($p in $prev.files.PSObject.Properties) { if ($p.Name -ne 'Windows') { $files[$p.Name] = $p.Value; "kept the $($p.Name) entry ($($p.Value.file))" } } }
$files['Windows'] = [ordered]@{ file = 'Recomp.exe'; sha256 = (Get-FileHash $exe.FullName -Algorithm SHA256).Hash.ToLower(); size = $exe.Length }
$manifest = [ordered]@{ version = $version; files = $files }
[IO.File]::WriteAllText("$Out\recomp-launcher.json", ($manifest | ConvertTo-Json -Depth 5), (New-Object Text.UTF8Encoding($false)))
"Recomp $version, {0:N1} MB" -f ($exe.Length / 1MB)
