# CassaSync - installazione su Windows come attività in background avviata al login
# (gira con il tuo utente, così può creare la cartella aperta nel tuo profilo).
#   powershell -ExecutionPolicy Bypass -File install.ps1             installa e avvia
#   powershell -ExecutionPolicy Bypass -File install.ps1 -Uninstall  rimuove (i dati cifrati restano)
param([switch]$Uninstall)
$ErrorActionPreference = 'Stop'
$AppDir  = Join-Path $env:LOCALAPPDATA 'CassaSync'
$BinDir  = Join-Path $AppDir 'bin'
$Exe     = Join-Path $BinDir 'cassasync.exe'
$Task    = 'CassaSync'
$Desktop = [Environment]::GetFolderPath('Desktop')
$StartMenu = Join-Path ([Environment]::GetFolderPath('Programs')) 'CassaSync.url'

function Stop-CassaSync {
  if (Get-ScheduledTask -TaskName $Task -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $Task -ErrorAction SilentlyContinue
  }
  Get-Process cassasync -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}

if ($Uninstall) {
  Stop-CassaSync
  Unregister-ScheduledTask -TaskName $Task -Confirm:$false -ErrorAction SilentlyContinue
  Remove-Item $Exe, (Join-Path $Desktop 'CassaSync.url'), $StartMenu -ErrorAction SilentlyContinue
  Write-Host "CassaSync rimosso. I dati cifrati restano in $AppDir."
  exit 0
}

$Src = Join-Path $PSScriptRoot 'cassasync.exe'
if (-not (Test-Path $Src)) { throw "Binario non trovato: $Src" }
Stop-CassaSync
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
Copy-Item $Src $Exe -Force
Unblock-File $Exe

$action    = New-ScheduledTaskAction -Execute $Exe -Argument 'client'
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
$settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
              -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
              -MultipleInstances IgnoreNew -Hidden
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName $Task -Action $action -Trigger $trigger -Settings $settings -Principal $principal `
  -Description 'CassaSync - cassaforte cifrata sincronizzata' -Force | Out-Null
Start-ScheduledTask -TaskName $Task

$url = "[InternetShortcut]`r`nURL=http://127.0.0.1:8484`r`nIconFile=%SystemRoot%\System32\imageres.dll`r`nIconIndex=54`r`n"
Set-Content -Path (Join-Path $Desktop 'CassaSync.url') -Value $url -Encoding ASCII
Set-Content -Path $StartMenu -Value $url -Encoding ASCII

Start-Sleep -Seconds 2
Write-Host 'CassaSync installato e avviato. Interfaccia: http://127.0.0.1:8484'
Start-Process 'http://127.0.0.1:8484'
