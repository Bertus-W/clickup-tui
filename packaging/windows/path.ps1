# Adds or removes a directory on the user's PATH, for the installer:
#   path.ps1 add|remove <dir>
# Works on the registry value itself so %VARIABLES% in PATH stay unexpanded, and has no length
# limit (NSIS's own string functions cut at 1024 characters, which could truncate PATH).
param([ValidateSet('add', 'remove')][string]$Action, [string]$Dir)
$ErrorActionPreference = 'Stop'
$key = Get-Item 'HKCU:\Environment'
$path = $key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
$parts = @($path -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -ne $Dir.TrimEnd('\') })
if ($Action -eq 'add') { $parts += $Dir }
Set-ItemProperty 'HKCU:\Environment' -Name Path -Value ($parts -join ';') -Type ExpandString
# Tell running programs (Explorer, new terminals) that the environment changed.
[Environment]::SetEnvironmentVariable('CU_INSTALLER_REFRESH', '1', 'User')
[Environment]::SetEnvironmentVariable('CU_INSTALLER_REFRESH', $null, 'User')
