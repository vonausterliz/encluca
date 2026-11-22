package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createNoWindow  = 0x08000000
	detachedProcess = 0x00000008
)

// toastScript mostra una notifica di Windows; titolo e testo arrivano da variabili d'ambiente
// (niente testo inserito nel codice PowerShell).
const toastScript = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$x = $t.GetElementsByTagName('text')
$x.Item(0).AppendChild($t.CreateTextNode($env:ENCLUCA_TITLE)) > $null
$x.Item(1).AppendChild($t.CreateTextNode($env:ENCLUCA_BODY)) > $null
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show([Windows.UI.Notifications.ToastNotification]::new($t))`

func notify(title, body string) error {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", toastScript)
	cmd.Env = append(os.Environ(), "ENCLUCA_TITLE="+title, "ENCLUCA_BODY="+body)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd.Start()
}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow | detachedProcess}
}

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	wtsapi32         = windows.NewLazySystemDLL("wtsapi32.dll")
	pRegisterClassEx = user32.NewProc("RegisterClassExW")
	pCreateWindowEx  = user32.NewProc("CreateWindowExW")
	pDefWindowProc   = user32.NewProc("DefWindowProcW")
	pGetMessage      = user32.NewProc("GetMessageW")
	pTranslateMsg    = user32.NewProc("TranslateMessage")
	pDispatchMsg     = user32.NewProc("DispatchMessageW")
	pWTSRegister     = wtsapi32.NewProc("WTSRegisterSessionNotification")
	pGetModuleHandle = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

type wndClassEx struct {
	Size, Style                    uint32
	WndProc                        uintptr
	ClsExtra, WndExtra             int32
	Instance, Icon, Cursor, Backgr windows.Handle
	MenuName, ClassName            *uint16
	IconSm                         windows.Handle
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	X, Y    int32
	Private uint32
}

const (
	wmWTSSessionChange = 0x02B1
	wtsSessionLock     = 0x7
	wmPowerBroadcast   = 0x0218
	pbtAPMSuspend      = 0x4
)

// watchLock usa una finestra nascosta (non "solo messaggi": quelle non ricevono
// WM_POWERBROADCAST) registrata per gli eventi della sessione.
func watchLock(ctx context.Context, onLock func()) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cls, _ := windows.UTF16PtrFromString("EnclucaTrayEvents")
	proc := windows.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		switch {
		case msg == wmWTSSessionChange && wp == wtsSessionLock,
			msg == wmPowerBroadcast && wp == pbtAPMSuspend:
			go onLock()
		}
		r, _, _ := pDefWindowProc.Call(hwnd, msg, wp, lp)
		return r
	})
	inst, _, _ := pGetModuleHandle.Call(0)
	wc := wndClassEx{WndProc: proc, ClassName: cls, Instance: windows.Handle(inst)}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return err
	}
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		return err
	}
	if r, _, err := pWTSRegister.Call(hwnd, 0 /* NOTIFY_FOR_THIS_SESSION */); r == 0 {
		return err
	}
	var m winMsg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return nil
		}
		pTranslateMsg.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMsg.Call(uintptr(unsafe.Pointer(&m)))
	}
}
