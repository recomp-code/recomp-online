# A game's pack for the Recomp launcher (installer.go): the folders of a built game (runtime\, gshost\, DLSS\ ...) in a
# zip with "/" paths, and the manifest the launcher reads from the game's GitHub release. Never the game itself: the
# script refuses ISOs, ELFs and GS dumps.
#   .\make_game_pack.ps1 -Id dbzbt4 -Version 2.2 -Source C:\...\DBZ_BT4_Windows -Include runtime,gshost,DLSS -Out <dir>
# Writes <Out>\<Zip> and <Out>\recomp-game.json; upload both to the release (the manifest lists every system's pack:
# run it once per system and merge with -Merge <the other recomp-game.json>).
param(
    [Parameter(Mandatory)][string]$Id,
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$Source,
    [string[]]$Include = @('runtime', 'gshost', 'DLSS'),
    [string]$Platform = 'Windows',
    [string]$Zip = '',
    [string]$Out = '.',
    [string]$LauncherMin = '3.0',
    [string]$Merge = ''
)
$ErrorActionPreference = 'Stop'
if (-not $Zip) { $Zip = "${Id}_$Platform.zip" }
New-Item -ItemType Directory -Force $Out | Out-Null
$zipPath = Join-Path (Resolve-Path $Out) $Zip
if (Test-Path $zipPath) { Remove-Item $zipPath }
$src = (Resolve-Path $Source).Path.TrimEnd('\')
$files = foreach ($inc in $Include) { if (Test-Path "$src\$inc") { Get-ChildItem "$src\$inc" -Recurse -File } }
$bad = $files | Where-Object { $_.Extension -in '.iso', '.elf', '.gs', '.map', '.lib' -or $_.Name -match '^(SLUS|SLES|SCUS|SCES|SLPS)_\d' }
if ($bad) { throw "the pack must not contain: $($bad.FullName -join ', ')" }
Add-Type -AssemblyName System.IO.Compression, System.IO.Compression.FileSystem
$zs = [IO.File]::Open($zipPath, [IO.FileMode]::CreateNew)
$za = New-Object IO.Compression.ZipArchive($zs, [IO.Compression.ZipArchiveMode]::Create)
foreach ($f in $files) {
    $name = $f.FullName.Substring($src.Length + 1).Replace('\', '/')
    [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($za, $f.FullName, $name, [IO.Compression.CompressionLevel]::Optimal) | Out-Null
}
$za.Dispose(); $zs.Dispose()
$hash = (Get-FileHash $zipPath -Algorithm SHA256).Hash.ToLower()
$size = (Get-Item $zipPath).Length
$packs = [ordered]@{}
if ($Merge -and (Test-Path $Merge)) {
    $old = Get-Content $Merge -Raw | ConvertFrom-Json
    foreach ($p in $old.packs.PSObject.Properties) { $packs[$p.Name] = $p.Value }
}
$packs[$Platform] = [ordered]@{ file = $Zip; sha256 = $hash; size = $size }
$manifest = [ordered]@{ id = $Id; version = $Version; launcherMin = $LauncherMin; packs = $packs }
[IO.File]::WriteAllText((Join-Path (Resolve-Path $Out) 'recomp-game.json'), ($manifest | ConvertTo-Json -Depth 5), (New-Object Text.UTF8Encoding($false)))
"{0} {1:N1} MB, {2} files, sha256 {3}" -f $Zip, ($size / 1MB), @($files).Count, $hash
