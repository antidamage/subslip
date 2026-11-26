<#
Install SubSlip on Windows and run it in the background at logon.

Downloads the release zip for this CPU from GitHub, checks it against
SHA256SUMS, writes settings with `subslip init` (an existing file is kept),
and registers a scheduled task that starts SubSlip at logon with no window and
starts it again within five minutes if it stops. Re-running upgrades the binary.

  irm https://raw.githubusercontent.com/antidamage/subslip/main/install/install.ps1 | iex
  .\install.ps1 -Upstream http://127.0.0.1:4533 -Version 0.1.0
  .\install.ps1 -NoService          # binary and settings only
  .\install.ps1 -OpenFirewall       # also allow inbound TCP on the port (needs admin)

Works in Windows PowerShell 5.1 and PowerShell 7.
#>
[CmdletBinding()]
param(
    [string]$Version = $(if ($env:SUBSLIP_VERSION) { $env:SUBSLIP_VERSION } else { 'latest' }),
    [string]$Upstream = $env:SUBSLIP_UPSTREAM,
    [string]$Listen = $(if ($env:SUBSLIP_LISTEN) { $env:SUBSLIP_LISTEN } else { '0.0.0.0:5276' }),
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'SubSlip'),
    [switch]$NoService,
    [switch]$OpenFirewall
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$Repo = 'antidamage/subslip'
$TaskName = 'SubSlip'
function Say([string]$m) { Write-Output "subslip-install: $m" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' } 'ARM64' { 'arm64' } 'x86' { '386' }
    default { throw "no release build for $env:PROCESSOR_ARCHITECTURE" }
}
if ($Version -eq 'latest') {
    $Version = (Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing).tag_name
}
$Version = $Version.TrimStart('v')
$asset = "subslip_${Version}_windows_$arch.zip"
$base = "https://github.com/$Repo/releases/download/v$Version"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("subslip-" + [guid]::NewGuid())
New-Item -ItemType Directory -Force $tmp, $InstallDir, (Join-Path $InstallDir 'data'), (Join-Path $InstallDir 'logs') | Out-Null
try {
    Say "downloading $asset"
    Invoke-WebRequest "$base/$asset" -OutFile (Join-Path $tmp $asset) -UseBasicParsing
    Invoke-WebRequest "$base/SHA256SUMS" -OutFile (Join-Path $tmp 'SHA256SUMS') -UseBasicParsing
    $line = Get-Content (Join-Path $tmp 'SHA256SUMS') | Where-Object { $_ -match "\s\*?$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $line) { throw "$asset is not listed in SHA256SUMS" }
    $want = ($line -split '\s+')[0].ToLowerInvariant()
    $got = (Get-FileHash (Join-Path $tmp $asset) -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($want -ne $got) { throw "checksum mismatch for $asset" }
    Expand-Archive (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'x') -Force

    # Stop a running copy so the binary can be replaced.
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) { Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue }
    Get-Process subslip -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$InstallDir*" } | Stop-Process -Force
    Copy-Item (Join-Path $tmp 'x\*') $InstallDir -Recurse -Force
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
$exe = Join-Path $InstallDir 'subslip.exe'
$envFile = Join-Path $InstallDir '.env'
Say "installed $(& $exe version) to $exe"

if (Test-Path $envFile) {
    Say "keeping existing settings in $envFile"
} else {
    $initArgs = @('init', '--env', $envFile, '--listen', $Listen)
    if ($Upstream) { $initArgs += @('--upstream', $Upstream) }
    & $exe @initArgs
    if ($LASTEXITCODE) { throw 'subslip init failed; pass -Upstream with your server''s URL' }
    (Get-Content $envFile) -replace '^LOG_FILE=.*$', ("LOG_FILE=" + (Join-Path $InstallDir 'logs\subslip.log')) | Set-Content $envFile -Encoding ascii
}

if (-not $NoService) {
    # conhost --headless runs the console program with no window at all.
    $action = New-ScheduledTaskAction -Execute 'conhost.exe' -Argument "--headless `"$exe`" serve --env `"$envFile`"" -WorkingDirectory $InstallDir
    $logon = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
    $again = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 5)
    $settings = New-ScheduledTaskSettingsSet -Hidden -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $logon, $again -Settings $settings `
        -Description 'SubSlip Subsonic proxy. Started again every 5 minutes if it has stopped.' -Force | Out-Null
    Start-ScheduledTask -TaskName $TaskName
    Say "registered and started the scheduled task '$TaskName'"
    Start-Sleep -Seconds 2
}

if ($OpenFirewall) {
    $port = ($Listen -split ':')[-1]
    New-NetFirewallRule -DisplayName "SubSlip $port" -Direction Inbound -Protocol TCP -LocalPort $port -Action Allow | Out-Null
    Say "opened inbound TCP $port"
}

Say "checking the installation (add a login with: & '$exe' doctor --env '$envFile' --user NAME --password PASS)"
& $exe doctor --env $envFile
