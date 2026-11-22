package main

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	"github.com/godbus/dbus/v5"
)

// notify mostra una notifica del desktop (org.freedesktop.Notifications).
func notify(title, body string) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.Call("org.freedesktop.Notifications.Notify", 0, "Encluca", uint32(0), "",
		title, body, []string{}, map[string]dbus.Variant{}, int32(-1)).Err
}

// watchLock chiama onLock quando lo schermo si blocca o il computer sta per sospendersi.
// Fonti: screensaver sul bus di sessione (freedesktop, GNOME, XFCE, light-locker) e
// systemd-logind sul bus di sistema (blocco della sessione, sospensione).
func watchLock(ctx context.Context, onLock func()) error {
	ch := make(chan *dbus.Signal, 16)
	sources := 0
	if sess, err := dbus.ConnectSessionBus(); err == nil {
		defer sess.Close()
		for _, iface := range []string{"org.freedesktop.ScreenSaver", "org.gnome.ScreenSaver", "org.xfce.ScreenSaver"} {
			if sess.AddMatchSignal(dbus.WithMatchInterface(iface), dbus.WithMatchMember("ActiveChanged")) == nil {
				sources++
			}
		}
		sess.Signal(ch)
	}
	var sys *dbus.Conn
	if c, err := dbus.ConnectSystemBus(); err == nil {
		sys = c
		defer sys.Close()
		if sys.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.login1.Session"), dbus.WithMatchMember("Lock")) == nil {
			sources++
		}
		if sys.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.login1.Manager"), dbus.WithMatchMember("PrepareForSleep")) == nil {
			sources++
		}
		sys.Signal(ch)
	}
	if sources == 0 {
		return errNoLockSource
	}
	// inibitore "delay": logind aspetta (qualche secondo) che la cassaforte sia chiusa
	// prima di sospendere
	inhibit := func() *os.File {
		if sys == nil {
			return nil
		}
		var fd dbus.UnixFD
		if err := sys.Object("org.freedesktop.login1", "/org/freedesktop/login1").Call(
			"org.freedesktop.login1.Manager.Inhibit", 0, "sleep", "Encluca",
			T("Chiusura della cassaforte prima della sospensione"), "delay").Store(&fd); err != nil {
			return nil
		}
		return os.NewFile(uintptr(fd), "inhibit")
	}
	inh := inhibit()
	for {
		select {
		case <-ctx.Done():
			if inh != nil {
				inh.Close()
			}
			return nil
		case sig, ok := <-ch:
			if !ok {
				return nil
			}
			first := len(sig.Body) > 0 && sig.Body[0] == true
			switch sig.Name {
			case "org.freedesktop.ScreenSaver.ActiveChanged", "org.gnome.ScreenSaver.ActiveChanged", "org.xfce.ScreenSaver.ActiveChanged":
				if first {
					onLock()
				}
			case "org.freedesktop.login1.Session.Lock":
				onLock()
			case "org.freedesktop.login1.Manager.PrepareForSleep":
				if first {
					onLock()
					if inh != nil {
						inh.Close() // fatto: la sospensione può procedere
						inh = nil
					}
				} else if inh == nil {
					inh = inhibit() // ripresa: pronto per la prossima sospensione
				}
			}
		}
	}
}

// detach stacca il processo figlio dalla sessione del padre.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
