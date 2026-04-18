<#
.SYNOPSIS
    Registers the Interaction Engine as a Windows Task Scheduler task
    that starts automatically when the current user logs on.

.DESCRIPTION
    Creates a scheduled task under \InteractionEngine\engine that:
      - Triggers at logon for the current user
      - Restarts up to 5 times (1-minute interval) on failure
      - Runs indefinitely (no execution time limit)
      - Logs stdout + stderr to engine.log in the install directory

    Requires Node.js >= 18 and npm to be on PATH.

.PARAMETER Port
    WebSocket listening port (default: 8079)

.PARAMETER HostAddr
    Bind address (default: 127.0.0.1)

.PARAMETER Uninstall
    Remove the scheduled task instead of creating it.

.EXAMPLE
    .\install-windows.ps1
    .\install-windows.ps1 -Port 9000 -HostAddr 0.0.0.0
    .\install-windows.ps1 -Uninstall
#>
#Requires -Version 5.1

param(
    [string] $Port      = '8079',
    [string] $HostAddr  = '127.0.0.1',
    [switch] $Uninstall
)

$ErrorActionPreference = 'Stop'
$TaskPath = '\InteractionEngine\'
$TaskName = 'engine'

# ── Uninstall path ────────────────────────────────────────────────────────────
if ($Uninstall) {
    try {
        Stop-ScheduledTask  -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -Confirm:$false
        Write-Host 'Task removed.'
    } catch {
        Write-Warning "Could not remove task: $_"
    }
    exit 0
}

# ── Pre-flight checks ─────────────────────────────────────────────────────────
$NodeBin = (Get-Command node -ErrorAction SilentlyContinue)?.Source
if (-not $NodeBin) { throw 'node.exe not found in PATH. Install Node.js >= 18 from https://nodejs.org' }

$NodeVer = & $NodeBin -e 'process.stdout.write(process.version)' 2>$null
$NodeMajor = [int]($NodeVer -replace 'v(\d+)\..*', '$1')
if ($NodeMajor -lt 18) { throw "Node.js >= 18 required. Found: $NodeVer" }

$NpmBin = (Get-Command npm -ErrorAction SilentlyContinue)?.Source
if (-not $NpmBin) { throw 'npm not found in PATH.' }

$InstallDir = Split-Path -Parent $PSScriptRoot
$ScriptPath = Join-Path $InstallDir 'engine-start.js'
$LogFile    = Join-Path $InstallDir 'engine.log'

if (-not (Test-Path $ScriptPath)) {
    throw "engine-start.js not found at: $ScriptPath"
}

Write-Host 'Installing Interaction Engine scheduled task'
Write-Host "  install dir : $InstallDir"
Write-Host "  node binary : $NodeBin"
Write-Host "  log file    : $LogFile"

# ── Install ws package if missing ─────────────────────────────────────────────
$WsDir = Join-Path $InstallDir 'node_modules\ws'
if (-not (Test-Path $WsDir)) {
    Write-Host '  Installing ws npm package...'
    Push-Location $InstallDir
    try   { & $NpmBin install ws --save | ForEach-Object { "    $_" } | Write-Host }
    finally { Pop-Location }
}

# ── Build the scheduled task ──────────────────────────────────────────────────
# Use cmd /c so we can set env vars and redirect output to a log file in one shot.
$CmdLine  = "/c set PORT=$Port& set HOST=$HostAddr& set NODE_ENV=production& set LOG_LEVEL=info& "
$CmdLine += "`"$NodeBin`" `"$ScriptPath`" >> `"$LogFile`" 2>&1"

$Action = New-ScheduledTaskAction `
    -Execute      'cmd.exe' `
    -Argument     $CmdLine `
    -WorkingDirectory $InstallDir

$Trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME

$Settings = New-ScheduledTaskSettingsSet `
    -ExecutionTimeLimit   ([TimeSpan]::Zero) `
    -RestartCount         5 `
    -RestartInterval      (New-TimeSpan -Minutes 1) `
    -StartWhenAvailable `
    -MultipleInstances    IgnoreNew

$Principal = New-ScheduledTaskPrincipal `
    -UserId   $env:USERNAME `
    -LogonType Interactive `
    -RunLevel Highest

Register-ScheduledTask `
    -TaskPath  $TaskPath `
    -TaskName  $TaskName `
    -Action    $Action `
    -Trigger   $Trigger `
    -Settings  $Settings `
    -Principal $Principal `
    -Force | Out-Null

Write-Host ''
Write-Host 'Done. The engine starts automatically at next logon.'
Write-Host ''
Write-Host 'Useful commands:'
Write-Host "  Start-ScheduledTask  -TaskPath '$TaskPath' -TaskName '$TaskName'"
Write-Host "  Stop-ScheduledTask   -TaskPath '$TaskPath' -TaskName '$TaskName'"
Write-Host "  Get-ScheduledTaskInfo -TaskPath '$TaskPath' -TaskName '$TaskName'"
Write-Host "  Get-Content '$LogFile' -Wait    # live log tail"
Write-Host "  .\install-windows.ps1 -Uninstall"
