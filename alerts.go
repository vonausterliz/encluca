package main

import "time"

// backupInfo è lo stato del backup su Google Drive che l'hub comunica ai dispositivi
// (solo data ed esito: nessun nome di file né altro della cassaforte).
type backupInfo struct {
	Enabled bool      `json:"enabled"` // backup autorizzato sul NAS
	LastOK  time.Time `json:"lastOk"`
	Error   string    `json:"error,omitempty"`
}
