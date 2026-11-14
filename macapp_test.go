package main

import "testing"

func TestAppBundleOf(t *testing.T) {
	cases := map[string]string{
		"/Applications/Encluca.app/Contents/MacOS/encluca":         "/Applications/Encluca.app",
		"/Users/x/Applications/Encluca.app/Contents/MacOS/encluca": "/Users/x/Applications/Encluca.app",
		"/Users/x/Library/Application Support/Encluca/bin/encluca": "",
		"/Applications/Encluca.app/Contents/Resources/encluca":     "",
		"/opt/Strano.app/Contents/MacOS/sotto/encluca":             "",
		"": "",
	}
	for in, want := range cases {
		if got := appBundleOf(in); got != want {
			t.Errorf("appBundleOf(%q) = %q, voglio %q", in, got, want)
		}
	}
}
