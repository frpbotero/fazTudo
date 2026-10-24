//go:build desktop && darwin

package main

import "testing"

func TestMacBundleOfFindsTheApp(t *testing.T) {
	if got, ok := macBundleOf("/Applications/FazTudo.app/Contents/MacOS/FazTudo"); !ok || got != "/Applications/FazTudo.app" {
		t.Errorf("bundle = %q, %v", got, ok)
	}
	if _, ok := macBundleOf("/Users/me/go/bin/faztudo"); ok {
		t.Error("a bare binary has no bundle: start it directly")
	}
}
