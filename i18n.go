package main

import "sync/atomic"

// Lingua dei messaggi rivolti all'utente (registro, errori, avvisi).
// Il testo di riferimento è l'italiano: T restituisce la traduzione inglese se la lingua è "en".
// TestTranslationsComplete verifica che ogni T("...") del codice abbia la sua traduzione.

var curLang atomic.Value // "it" | "en"

func setLang(l string) {
	if l != "en" {
		l = "it"
	}
	curLang.Store(l)
}

func getLang() string {
	if l, ok := curLang.Load().(string); ok {
		return l
	}
	return "it"
}

func validLang(l string) bool { return l == "it" || l == "en" }

// T traduce un messaggio (anche una stringa di formato) nella lingua corrente.
func T(it string) string {
	if getLang() == "en" {
		if en, ok := enMessages[it]; ok {
			return en
		}
	}
	return it
}

var enMessages = map[string]string{
	// client: collegamento e sicurezza
	"nessun certificato dal NAS":                                     "no certificate from the NAS",
	"il certificato del NAS non corrisponde a quello abbinato":       "the NAS certificate does not match the paired one",
	"ERRORE salvataggio stato: %v":                                   "ERROR saving state: %v",
	"questo dispositivo è stato revocato dal NAS: abbinalo di nuovo": "this device has been revoked by the NAS: pair it again",
	"dispositivo revocato":                                           "device revoked",
	"NAS raggiungibile":                                              "NAS reachable",
	"NAS non raggiungibile: le modifiche restano in coda":            "NAS unreachable: changes stay queued",
	"codice di abbinamento non valido":                               "invalid pairing code",
	"chiudi la cassaforte prima di cambiare NAS":                     "close the vault before changing NAS",
	"impossibile contattare il NAS: %w":                              "cannot reach the NAS: %w",
	"Abbinato al NAS %s come %q":                                     "Paired with NAS %s as %q",
	"questo computer non è collegato a un NAS":                       "this computer is not paired with a NAS",

	// client: sincronizzazione
	"Modificato: %s":                                   "Changed: %s",
	"la cartella aperta non esiste più: ":              "the open folder no longer exists: ",
	"nome troppo lungo per essere cifrato: accorcialo": "name too long to be encrypted: shorten it",
	"non leggibile: ":                                  "not readable: ",
	"ATTENZIONE: %d file spariti insieme dalla cartella aperta. Eliminazione sospesa: confermala dall'interfaccia.": "WARNING: %d files disappeared at once from the open folder. Deletion on hold: confirm it from the interface.",
	"Eliminato: %s":            "Deleted: %s",
	"nome non decifrabile: %w": "name cannot be decrypted: %w",
	"CONFLITTO: %s modificato anche altrove. La tua versione è salvata come %q": "CONFLICT: %s was also changed elsewhere. Your version is saved as %q",
	"Eliminato da un altro dispositivo: %s":                                     "Deleted by another device: %s",
	"download corrotto, riprovo":                                                "corrupted download, retrying",
	"ERRORE decifratura %s: %v":                                                 "ERROR decrypting %s: %v",
	"Aggiornato da un altro dispositivo: %s":                                    "Updated by another device: %s",
	"Sincronizzazione interrotta: %v":                                           "Sync interrupted: %v",
	"Errore dal NAS: %v":                                                        "Error from the NAS: %v",

	// client: cassaforte
	"la password deve avere almeno 10 caratteri":                                                      "the password must be at least 10 characters long",
	"la cassaforte esiste già: usa Apri":                                                              "the vault already exists: use Open",
	"Cassaforte creata":                                                                               "Vault created",
	"la cassaforte non è ancora stata creata":                                                         "the vault has not been created yet",
	"password errata":                                                                                 "wrong password",
	"la cartella %s esiste già e non è vuota: spostala o svuotala":                                    "the folder %s already exists and is not empty: move or empty it",
	"ERRORE decifratura di un file: %v":                                                               "ERROR decrypting a file: %v",
	"Cassaforte riaperta (ripresa dopo un'interruzione): %s":                                          "Vault reopened (resumed after an interruption): %s",
	"Cassaforte aperta in %s":                                                                         "Vault open in %s",
	"la cassaforte era rimasta aperta: inserisci la password e premi Apri, poi chiudi":                "the vault was left open: enter the password and press Open, then close it",
	"non chiudo: %v (chiudi le applicazioni che usano i file e riprova)":                              "not closing: %v (close the applications using the files and try again)",
	"ci sono %d eliminazioni in attesa di conferma: confermale o ripristina i file prima di chiudere": "%d deletions are waiting for confirmation: confirm them or restore the files before closing",
	"non chiudo: %d file non sono nella cassaforte e andrebbero persi (%s). Rinominali o spostali fuori dalla cartella, poi chiudi": "not closing: %d files are not in the vault and would be lost (%s). Rename them or move them out of the folder, then close",
	"alcuni file in chiaro non sono stati cancellati: ":                                                                             "some plaintext files were not deleted: ",
	"Cassaforte chiusa: cartella in chiaro cancellata":                                                                              "Vault closed: plaintext folder deleted",
	"la cassaforte è chiusa":                          "the vault is closed",
	"indica un percorso assoluto":                     "enter an absolute path",
	"chiudi la cassaforte prima di cambiare cartella": "close the vault before changing folder",
	"La cassaforte era rimasta aperta (riavvio?): apri con la password per riprendere e poi chiudi": "The vault was left open (restart?): open it with the password to resume, then close it",

	// client: rinomina
	"apri la cassaforte per rinominare":                                         "open the vault to rename",
	"questo elemento non è più da rinominare":                                   "this item no longer needs renaming",
	"nome non valido: niente barre e non vuoto":                                 "invalid name: no slashes and not empty",
	"anche questo nome è troppo lungo: accorcialo ancora":                       "this name is too long as well: shorten it further",
	"esiste già un elemento chiamato %q nella cassaforte: scegli un altro nome": "an item named %q already exists in the vault: choose another name",
	"Rinominato: %s → %s":                                                       "Renamed: %s → %s",

	// servizio
	"l'interfaccia del client può ascoltare solo su 127.0.0.1/localhost": "the client interface can only listen on 127.0.0.1/localhost",
	"Encluca %s avviato: apri http://%s":                                 "Encluca %s started: open http://%s",
	"Arresto del servizio: chiudo la cassaforte":                         "Service stopping: closing the vault",
	"Chiusura all'arresto non riuscita: %v":                              "Closing on shutdown failed: %v",
	"richiesta non valida":                                               "invalid request",

	// hub
	"config hub illeggibile: %w":                                 "unreadable hub config: %w",
	"Password amministratore iniziale: %s (salvata anche in %s)": "Initial administrator password: %s (also saved in %s)",
	"indice illeggibile: %w":                                     "unreadable index: %w",
	"Indice ricostruito: %d file":                                "Index rebuilt: %d files",
	"dispositivo non autorizzato":                                "device not authorized",
	"cassaforte non ancora creata":                               "vault not created yet",
	"metadati non validi":                                        "invalid metadata",
	"la cassaforte esiste già":                                   "the vault already exists",
	"Cassaforte creata dal dispositivo %q":                       "Vault created by device %q",
	"metodo non ammesso":                                         "method not allowed",
	"percorso non valido":                                        "invalid path",
	"non trovato":                                                "not found",
	"upload interrotto":                                          "upload interrupted",
	"checksum non corrispondente":                                "checksum mismatch",
	"ERRORE salvataggio indice: %v":                              "ERROR saving index: %v",
	"accesso richiesto":                                          "login required",
	"Accesso amministratore fallito da %s":                       "Failed administrator login from %s",
	"nome dispositivo obbligatorio":                              "device name required",
	"Nuovo dispositivo abbinabile: %q":                           "New device ready to pair: %q",
	"Dispositivo revocato: %q":                                   "Device revoked: %q",
	"la nuova password deve avere almeno 10 caratteri":           "the new password must be at least 10 characters long",
	"password attuale errata":                                    "current password is wrong",
	"Password amministratore cambiata":                           "Administrator password changed",
	"Encluca hub %s in ascolto su https://%s (impronta certificato %s…)": "Encluca hub %s listening on https://%s (certificate fingerprint %s…)",

	// Google Drive
	"Backup Google Drive interrotto per l'arresto: riprenderà al riavvio": "Google Drive backup interrupted by shutdown: it will resume on restart",
	"funzione disattivata: attivala in Impostazioni":                      "feature disabled: enable it in Settings",
	"Esplorazione della cassaforte dal NAS attivata":                      "Vault browsing from the NAS enabled",
	"Esplorazione della cassaforte dal NAS disattivata":                   "Vault browsing from the NAS disabled",
	"Backup Google Drive fallito: %v":                                     "Google Drive backup failed: %v",
	"Backup Google Drive completato: %s":                                  "Google Drive backup completed: %s",
	"%d caricati, %d spostati nelle versioni":                             "%d uploaded, %d moved to versions",
	"token Google non ottenuto: %s %s":                                    "Google token not obtained: %s %s",
	"Google ha rifiutato la richiesta: %v %s %s":                          "Google rejected the request: %v %s %s",
	"autorizzazione Google non riuscita: ":                                "Google authorization failed: ",
	"Google Drive autorizzato":                                            "Google Drive authorized",
	"Drive non ha restituito l'URL di upload":                             "Drive did not return the upload URL",
	"Cartella di backup non più presente su Drive: la ricreo":             "Backup folder no longer on Drive: recreating it",
	"Versioni del %s eliminate (più vecchie di %d giorni)":                "Versions from %s deleted (older than %d days)",
	"dati non validi": "invalid data",
	"inserisci prima Client ID e Client Secret": "enter Client ID and Client Secret first",
	"Google Drive non è ancora autorizzato":     "Google Drive is not authorized yet",
	"Google Drive scollegato":                   "Google Drive disconnected",
}
