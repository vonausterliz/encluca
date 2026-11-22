package main

import (
	"strings"
	"testing"
)

func ids(list []alert) []string {
	var out []string
	for _, a := range list {
		out = append(out, a.ID)
	}
	return out
}

func TestTrayViewStates(t *testing.T) {
	if v := viewFor(trayState{}); v.Icon != "problem" || v.CanOpen || v.CanClose {
		t.Fatalf("servizio irraggiungibile: %+v", v)
	}
	if v := viewFor(trayState{Reachable: true, Status: trayStatus{Open: true}}); v.Icon != "open" || !v.CanClose || !v.CanFolder || v.CanOpen {
		t.Fatalf("aperta: %+v", v)
	}
	if v := viewFor(trayState{Reachable: true}); v.Icon != "closed" || !v.CanOpen || v.CanClose || v.CanFolder {
		t.Fatalf("chiusa: %+v", v)
	}
	warn := trayStatus{Alerts: []alert{{ID: "offline", Level: "warn", Text: "x"}}}
	if v := viewFor(trayState{Reachable: true, Status: warn}); v.Icon != "problem" {
		t.Fatalf("avviso warn: %+v", v)
	}
	info := trayStatus{Open: true, Alerts: []alert{{ID: "autoClose", Level: "info"}}}
	if v := viewFor(trayState{Reachable: true, Status: info}); v.Icon != "open" || !v.CanPostpone {
		t.Fatalf("chiusura automatica imminente: %+v", v)
	}
}

func TestTrayStatusLine(t *testing.T) {
	cases := []struct {
		s    trayState
		want string
	}{
		{trayState{}, "Servizio encluca non raggiungibile"},
		{trayState{Reachable: true, Status: trayStatus{Open: true, Pending: 3}}, "Cassaforte aperta · 3 in coda"},
		{trayState{Reachable: true, Status: trayStatus{Open: true}}, "Cassaforte aperta"},
		{trayState{Reachable: true, Status: trayStatus{StaleOpen: true}}, "Cassaforte rimasta aperta"},
		{trayState{Reachable: true}, "Cassaforte chiusa"},
	}
	for _, c := range cases {
		if got := viewFor(c.s).StatusLine; got != c.want {
			t.Errorf("riga di stato %q, voluta %q", got, c.want)
		}
	}
}

func TestTrayNotifiesExistingAtStart(t *testing.T) {
	var n trayNotifier
	show, _ := n.diff([]alert{{ID: "staleOpen", Level: "warn"}})
	if strings.Join(ids(show), ",") != "staleOpen" {
		t.Fatalf("al primo controllo: %v", ids(show))
	}
}

func TestTrayNotifyOncePerID(t *testing.T) {
	var n trayNotifier
	off := alert{ID: "offline", Level: "warn"}
	if show, _ := n.diff([]alert{off}); len(show) != 1 {
		t.Fatal("prima notifica mancante")
	}
	if show, _ := n.diff([]alert{off}); len(show) != 0 {
		t.Fatal("notifica ripetuta per lo stesso ID")
	}
	if _, res := n.diff(nil); strings.Join(ids(res), ",") != "offline" {
		t.Fatalf("risolto: %v", ids(res))
	}
	if show, _ := n.diff([]alert{{ID: "conflicts-1"}}); len(show) != 1 {
		t.Fatal("primo conflitto non notificato")
	}
	if show, _ := n.diff([]alert{{ID: "conflicts-2"}}); len(show) != 1 {
		t.Fatal("conflitto nuovo non notificato")
	}
	show, res := n.diff([]alert{{ID: "conflicts-1"}})
	if len(show) != 0 || len(res) != 0 {
		t.Fatalf("conflitto risolto (2 -> 1) notificato: show=%v res=%v", ids(show), ids(res))
	}
	if _, res := n.diff([]alert{{ID: "massDelete"}}); len(res) != 0 {
		t.Fatalf("risolto per un ID che non lo prevede: %v", ids(res))
	}
}

func TestTrayLockAction(t *testing.T) {
	off := false
	cases := []struct {
		s    trayState
		want bool
	}{
		{trayState{Reachable: true, Status: trayStatus{Open: true}}, true},
		{trayState{Reachable: true, Status: trayStatus{Open: true, CloseOnLock: &off}}, false},
		{trayState{Reachable: true}, false},
		{trayState{Status: trayStatus{Open: true}}, false},
	}
	for i, c := range cases {
		if got := lockAction(c.s); got != c.want {
			t.Errorf("caso %d: %v, voluto %v", i, got, c.want)
		}
	}
}

func TestTrayIcons(t *testing.T) {
	for _, s := range []string{"closed", "open", "problem"} {
		p := trayIconPNG(s)
		if len(p) < 100 || string(p[1:4]) != "PNG" {
			t.Fatalf("%s: PNG non valido", s)
		}
		ico := trayIconICO(s)
		if ico[2] != 1 || ico[4] != 1 || string(ico[23:26]) != "PNG" {
			t.Fatalf("%s: ICO non valido", s)
		}
	}
	if string(trayIconPNG("open")) == string(trayIconPNG("closed")) {
		t.Fatal("icone di stati diversi identiche")
	}
}

func TestTrayRelaunchTarget(t *testing.T) {
	if _, ok := relaunchTarget(trayStatus{Version: Version, Exe: "/x"}, ""); ok {
		t.Fatal("rilancio con la stessa versione")
	}
	if _, ok := relaunchTarget(trayStatus{}, ""); ok {
		t.Fatal("rilancio senza versione del servizio")
	}
	exe, ok := relaunchTarget(trayStatus{Version: "9.9.9", Exe: "/opt/encluca"}, "")
	if !ok || exe != "/opt/encluca" {
		t.Fatalf("rilancio verso il binario del servizio: %q %v", exe, ok)
	}
	// già rilanciato per questa versione e ancora diverso: niente ciclo
	if _, ok := relaunchTarget(trayStatus{Version: "9.9.9", Exe: "/opt/encluca"}, "9.9.9"); ok {
		t.Fatal("secondo rilancio per la stessa versione")
	}
}

func TestNotifyMarkupEscaped(t *testing.T) {
	if got := markupEscape("Q&A <bozza>.odt"); got != "Q&amp;A &lt;bozza&gt;.odt" {
		t.Fatalf("%q", got)
	}
}
