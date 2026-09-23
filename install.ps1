# Installs cu, the ClickUp terminal UI, from the latest Codeberg release (Windows):
#
#   irm https://codeberg.org/b-wisman/clickup-tui/raw/branch/main/install.ps1 | iex
#
# $env:CU_VERSION = "v0.1.0" picks a release. cu goes to %LocalAppData%\Programs\cu, which is
# added to your user PATH. The download is checked against the release's checksums.
$ErrorActionPreference = "Stop"
$repo = "b-wisman/clickup-tui"

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "unsupported CPU $env:PROCESSOR_ARCHITECTURE" }
}

$tag = $env:CU_VERSION
if (-not $tag) {
    $tag = (Invoke-RestMethod "https://codeberg.org/api/v1/repos/$repo/releases/latest").tag_name
}

$archive = "cu_$($tag.TrimStart('v'))_windows_$arch.zip"
$base = "https://codeberg.org/$repo/releases/download/$tag"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
try {
    Write-Host "Downloading cu $tag for windows/$arch..."
    Invoke-WebRequest "$base/$archive" -OutFile "$tmp\$archive" -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing

    $line = Get-Content "$tmp\checksums.txt" | Where-Object { $_ -match " $([regex]::Escape($archive))$" }
    $want = ($line -split " ")[0]
    $got = (Get-FileHash "$tmp\$archive" -Algorithm SHA256).Hash.ToLower()
    if (-not $want -or $want -ne $got) { throw "checksum mismatch for $archive" }

    $dir = Join-Path $env:LOCALAPPDATA "Programs\cu"
    New-Item -ItemType Directory -Force $dir | Out-Null
    Expand-Archive "$tmp\$archive" -DestinationPath $tmp -Force
    Copy-Item "$tmp\cu.exe" "$dir\cu.exe" -Force
} finally {
    Remove-Item -Recurse -Force $tmp
}

# The registry value itself, so %VARIABLES% in PATH stay as they are.
$userPath = (Get-Item 'HKCU:\Environment').GetValue('Path', '', 'DoNotExpandEnvironmentNames')
if (($userPath -split ";") -notcontains $dir) {
    $parts = @($userPath -split ";" | Where-Object { $_ }) + $dir
    Set-ItemProperty 'HKCU:\Environment' -Name Path -Value ($parts -join ";") -Type ExpandString
    # Tell running programs (Explorer, new terminals) that the environment changed.
    [Environment]::SetEnvironmentVariable("CU_INSTALLER_REFRESH", "1", "User")
    [Environment]::SetEnvironmentVariable("CU_INSTALLER_REFRESH", $null, "User")
    $env:Path += ";$dir"
    Write-Host "Added $dir to your PATH (new terminals pick it up)."
}
Write-Host "Installed $(& "$dir\cu.exe" version) to $dir\cu.exe"
Write-Host "Run cu to start: the first time it asks for your ClickUp API token."
