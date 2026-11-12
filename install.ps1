# Official Damn-Bot installer for Windows (GAP-SEC-19/GAP-OPS-03).
# Verifies the pinned ed25519 signature over SHA256SUMS and every checksum
# BEFORE extracting or running anything, then records each written path in
# %LOCALAPPDATA%\damnbot\install-manifest.json for `damnbot setup --uninstall`.
# Download -> verify -> run; this file is never piped from the network.
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2

$PubKey = if ($env:DAMNBOT_INSTALL_PUBKEY) { $env:DAMNBOT_INSTALL_PUBKEY } else { '__DAMNBOT_RELEASE_PUBKEY__' }
$BaseUrl = if ($env:DAMNBOT_INSTALL_BASE_URL) { $env:DAMNBOT_INSTALL_BASE_URL } else { 'https://github.com/damnbot/damnbot/releases' }
$Version = if ($env:DAMNBOT_VERSION) { $env:DAMNBOT_VERSION } else { 'latest' }
$BinDir = if ($env:DAMNBOT_BIN_DIR) { $env:DAMNBOT_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\damnbot\bin' }
$StateDir = Join-Path $env:LOCALAPPDATA 'damnbot'
$Begin = '# >>> damnbot >>>'
$End = '# <<< damnbot <<<'

function Fail([string]$Message) { Write-Error "install: $Message"; exit 1 }

function Get-Sha256([string]$Path) {
    (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Invoke-Download([string]$Url, [string]$Out) {
    $loopback = $Url -match '^http://(127\.0\.0\.1|localhost|\[::1\])([:/]|$)'
    if (-not ($Url -match '^https://' -or $loopback)) { Fail "refusing non-https URL: $Url" }
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $Out -MaximumRedirection 5 -ErrorAction Stop | Out-Null
    } catch {
        Fail "download failed: $Url ($($_.Exception.Message))"
    }
    if (-not (Test-Path -LiteralPath $Out)) { Fail "download failed: $Url" }
}

function Write-Pem([byte[]]$Raw, [string]$Path) {
    $der = [byte[]](0x30,0x2a,0x30,0x05,0x06,0x03,0x2b,0x65,0x70,0x03,0x21,0x00) + $Raw[10..41]
    $b64 = [Convert]::ToBase64String($der, [Base64FormattingOptions]::InsertLineBreaks)
    [IO.File]::WriteAllText($Path, "-----BEGIN PUBLIC KEY-----`n$b64`n-----END PUBLIC KEY-----`n")
}

function Verify-Minisig([string]$File, [string]$Sig) {
    $lines = @(Get-Content -LiteralPath $Sig)
    if ($lines.Count -lt 4) { Fail "malformed signature file: $Sig" }
    $sigRaw = [Convert]::FromBase64String($lines[1])
    $globalRaw = [Convert]::FromBase64String($lines[3])
    if ($sigRaw.Length -ne 74 -or $globalRaw.Length -ne 74) { Fail 'malformed minisign signature' }
    $pubRaw = [Convert]::FromBase64String($PubKey)
    if ($pubRaw.Length -ne 42) { Fail 'pinned public key is not a minisign key' }
    $openssl = Get-Command openssl -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($openssl) {
        $pem = Join-Path $work 'pub.pem'
        Write-Pem $pubRaw $pem
        $alg = [Text.Encoding]::ASCII.GetString([byte[]]$sigRaw[0..1])
        $msg = Join-Path $work 'msg'
        if ($alg -eq 'ED') {
            $global:LASTEXITCODE = 0
            & $openssl.Source dgst -blake2b512 -binary -out $msg $File
            if ($LASTEXITCODE -ne 0) { Fail "openssl dgst failed for $File" }
        } else { Copy-Item -LiteralPath $File -Destination $msg }
        $sigFile = Join-Path $work 'sig.bin'
        [IO.File]::WriteAllBytes($sigFile, [byte[]]$sigRaw[10..73])
        $global:LASTEXITCODE = 0
        & $openssl.Source pkeyutl -verify -rawin -pubin -inkey $pem -in $msg -sigfile $sigFile | Out-Null
        if ($LASTEXITCODE -ne 0) { Fail "signature verification failed for $File" }
        $globalMsg = Join-Path $work 'globalmsg'
        $trusted = $lines[2] -replace '^trusted comment: ', ''
        $comment = [Text.Encoding]::UTF8.GetBytes($trusted)
        $all = New-Object byte[] (64 + $comment.Length)
        [Array]::Copy($sigRaw, 10, $all, 0, 64)
        $comment.CopyTo($all, 64)
        [IO.File]::WriteAllBytes($globalMsg, $all)
        $globalSig = Join-Path $work 'global.bin'
        [IO.File]::WriteAllBytes($globalSig, [byte[]]$globalRaw[10..73])
        $global:LASTEXITCODE = 0
        & $openssl.Source pkeyutl -verify -rawin -pubin -inkey $pem -in $globalMsg -sigfile $globalSig | Out-Null
        if ($LASTEXITCODE -ne 0) { Fail "trusted comment signature failed for $File" }
        return
    }
    $minisign = Get-Command minisign -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $minisign) { Fail 'verification requires minisign or openssl in PATH' }
    $global:LASTEXITCODE = 0
    & $minisign.Source -Vm $File -P $PubKey | Out-Null
    if ($LASTEXITCODE -ne 0) { Fail "signature verification failed for $File" }
}

function Find-Bytes([byte[]]$Hay, [byte[]]$Needle, [int]$From = 0) {
    if ($Needle.Length -eq 0 -or $Hay.Length -lt $Needle.Length) { return -1 }
    for ($i = $From; $i -le $Hay.Length - $Needle.Length; $i++) {
        $hit = $true
        for ($j = 0; $j -lt $Needle.Length; $j++) { if ($Hay[$i + $j] -ne $Needle[$j]) { $hit = $false; break } }
        if ($hit) { return $i }
    }
    return -1
}

function Update-Profile([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) { return }
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Path) | Out-Null
    $before = $null
    $content = [byte[]]@()
    if (Test-Path -LiteralPath $Path) {
        $before = Get-Sha256 $Path
        $content = [IO.File]::ReadAllBytes($Path)
    }
    $beginBytes = [Text.Encoding]::UTF8.GetBytes("$Begin`n")
    $endBytes = [Text.Encoding]::UTF8.GetBytes("$End`n")
    $start = Find-Bytes $content $beginBytes
    if ($start -ge 0) {
        $endAt = Find-Bytes $content $endBytes $start
        if ($endAt -lt 0) { Fail "unbalanced damnbot markers in $Path" }
        $cutStart = $start
        if ($start -gt 0 -and $content[$start - 1] -eq 10) { $cutStart = $start - 1 }
        $head = New-Object byte[] $cutStart
        [Array]::Copy($content, 0, $head, 0, $cutStart)
        $tail = New-Object byte[] ($content.Length - ($endAt + $endBytes.Length))
        [Array]::Copy($content, $endAt + $endBytes.Length, $tail, 0, $tail.Length)
        $content = $head + $tail
    }
    $added = $false
    if ($content.Length -gt 0 -and $content[$content.Length - 1] -ne 10) { $content += [byte]10; $added = $true }
    $body = "`$env:Path = `"$BinDir;`" + `$env:Path"
    $block = [Text.Encoding]::UTF8.GetBytes("$Begin`n$body`n$End`n")
    [IO.File]::WriteAllBytes($Path, [byte[]]($content + $block))
    return [ordered]@{ Path = $Path; Before = $before; AddedNewline = $added }
}

$work = Join-Path ([IO.Path]::GetTempPath()) ('damnbot-install-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $work | Out-Null
try {
    $arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
    $rel = if ($Version -eq 'latest') { "$BaseUrl/latest/download" } else { "$BaseUrl/v$Version" }
    Write-Host "install: downloading $Version artifacts"
    Invoke-Download "$rel/SHA256SUMS" (Join-Path $work 'SHA256SUMS')
    Invoke-Download "$rel/SHA256SUMS.minisig" (Join-Path $work 'SHA256SUMS.minisig')
    if ($PubKey -eq '__DAMNBOT_RELEASE_PUBKEY__') { Fail 'no pinned release key in this checkout' }
    Verify-Minisig (Join-Path $work 'SHA256SUMS') (Join-Path $work 'SHA256SUMS.minisig')

    $sums = Get-Content -LiteralPath (Join-Path $work 'SHA256SUMS')
    $suffix = "_windows_${arch}.zip"
    $candidates = @($sums | ForEach-Object {
        $parts = $_ -split '\s+', 2
        $name = $parts[1].TrimStart('*')
        if ($name -match '^damnbot_[A-Za-z0-9._-]+_windows_(amd64|arm64)\.zip$' -and $name.EndsWith($suffix)) {
            [pscustomobject]@{ Name = $name; Sha = $parts[0] }
        }
    })
    if ($candidates.Count -ne 1) { Fail "release has no unique windows/$arch archive" }
    $asset = $candidates[0]
    $archive = Join-Path $work $asset.Name
    Invoke-Download "$rel/$($asset.Name)" $archive
    if ((Get-Sha256 $archive) -ne $asset.Sha.ToLowerInvariant()) { Fail "checksum mismatch for $($asset.Name)" }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($archive)
    try {
        foreach ($entry in $zip.Entries) {
            if ($entry.FullName -ne 'damnbot.exe' -and $entry.FullName -ne './damnbot.exe') {
                Fail "unsafe path in archive: $($entry.FullName)"
            }
        }
    } finally { $zip.Dispose() }
    $extract = Join-Path $work 'x'
    [IO.Compression.ZipFile]::ExtractToDirectory($archive, $extract)
    $newBin = Join-Path $extract 'damnbot.exe'
    if (-not (Test-Path -LiteralPath $newBin)) { Fail 'archive has no damnbot.exe' }
    if ((Get-Item -LiteralPath $newBin).Attributes -match 'ReparsePoint') { Fail 'archive binary is a reparse point' }

    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    $target = Join-Path $BinDir 'damnbot.exe'
    $before = if (Test-Path -LiteralPath $target) { Get-Sha256 $target } else { $null }
    $staged = Join-Path $BinDir ('.damnbot.new.' + [guid]::NewGuid().ToString('N'))
    Copy-Item -LiteralPath $newBin -Destination $staged
    Move-Item -LiteralPath $staged -Destination $target -Force
    $files = @([ordered]@{ path = $target; kind = 'binary'; sha256_before = $before; sha256_after = (Get-Sha256 $target) })
    $blocks = @()
    $profileEntry = Update-Profile $PROFILE
    if ($profileEntry) {
        $files += [ordered]@{ path = $profileEntry.Path; kind = 'rc'; sha256_before = $profileEntry.Before; sha256_after = (Get-Sha256 $profileEntry.Path) }
        $blocks += [ordered]@{ path = $profileEntry.Path; begin = $Begin; end = $End; added_newline = [bool]$profileEntry.AddedNewline }
    }
    New-Item -ItemType Directory -Force -Path $StateDir | Out-Null
    $manifest = [ordered]@{
        schema = 1; version = $Version; state_dir = $StateDir
        installed_at = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
        files = $files; blocks = $blocks; dirs = @($BinDir, $StateDir)
    }
    $manifestPath = Join-Path $StateDir 'install-manifest.json'
    $tmpManifest = Join-Path $StateDir ('.install-manifest.' + [guid]::NewGuid().ToString('N'))
    [IO.File]::WriteAllText($tmpManifest, ($manifest | ConvertTo-Json -Depth 6))
    $acl = Get-Command icacls.exe -ErrorAction SilentlyContinue
    if ($acl) { & $acl.Source $tmpManifest /inheritance:r /grant:r "$($env:USERNAME):(F)" | Out-Null }
    Move-Item -LiteralPath $tmpManifest -Destination $manifestPath -Force
    Write-Host "install: $Version installed to $target"
    Write-Host "install: manifest $manifestPath; run ``damnbot setup --uninstall`` to remove"
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
