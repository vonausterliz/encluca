package main

// Chiave pubblica dei rilasci; la privata è in
// ~/.config/encluca-release/signing.key (fuori dal repo).
// È una var stringa così i test end-to-end possono sovrascriverla con
// -ldflags "-X main.releasePubKeyHex=…".
var releasePubKeyHex = "fb93d36ddee5df4305c20f6c56105a73162720a4d50de462211cd28e19d0db81"
