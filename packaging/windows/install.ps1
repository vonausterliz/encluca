# Encluca - installazione su Windows come attività in background avviata al login
# (gira con il tuo utente, così può creare la cartella aperta nel tuo profilo).
#   powershell -ExecutionPolicy Bypass -File install.ps1             installa e avvia
#   powershell -ExecutionPolicy Bypass -File install.ps1 -Uninstall  rimuove (i dati cifrati restano)
param([switch]$Uninstall)
$ErrorActionPreference = 'Stop'
$AppDir  = Join-Path $env:LOCALAPPDATA 'Encluca'
$BinDir  = Join-Path $AppDir 'bin'
$Exe     = Join-Path $BinDir 'encluca.exe'
$Task    = 'Encluca'
$Desktop = [Environment]::GetFolderPath('Desktop')
$StartMenu = Join-Path ([Environment]::GetFolderPath('Programs')) 'Encluca.url'

function Stop-Encluca {
  if (Get-ScheduledTask -TaskName $Task -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $Task -ErrorAction SilentlyContinue
  }
  Get-Process encluca -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}

if ($Uninstall) {
  Stop-Encluca
  Unregister-ScheduledTask -TaskName $Task -Confirm:$false -ErrorAction SilentlyContinue
  Remove-Item $Exe, (Join-Path $AppDir 'encluca.ico'), (Join-Path $Desktop 'Encluca.url'), $StartMenu -ErrorAction SilentlyContinue
  Write-Host "Encluca rimosso. I dati cifrati restano in $AppDir."
  exit 0
}

$Src = Join-Path $PSScriptRoot 'encluca.exe'
if (-not (Test-Path $Src)) { throw "Binario non trovato: $Src" }
Stop-Encluca
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
  -Description 'Encluca - cassaforte cifrata sincronizzata' -Force | Out-Null
Start-ScheduledTask -TaskName $Task

$Ico = Join-Path $AppDir 'encluca.ico'
$IcoSrc = Join-Path $PSScriptRoot 'encluca.ico'
if (Test-Path $IcoSrc) { Copy-Item $IcoSrc $Ico -Force }
$url = "[InternetShortcut]`r`nURL=http://127.0.0.1:8484`r`nIconFile=$Ico`r`nIconIndex=0`r`n"
Set-Content -Path (Join-Path $Desktop 'Encluca.url') -Value $url -Encoding ASCII
Set-Content -Path $StartMenu -Value $url -Encoding ASCII

Start-Sleep -Seconds 2
Write-Host 'Encluca installato e avviato. Interfaccia: http://127.0.0.1:8484'
Start-Process 'http://127.0.0.1:8484'
