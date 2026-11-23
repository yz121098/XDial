package store

import (
	"testing"
)

func TestStoreDPAPIRoundTripAndPreferences(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New store failed: %v", err)
	}

	lib, err := st.LoadLibrary()
	if err != nil {
		t.Fatalf("LoadLibrary failed: %v", err)
	}
	if len(lib) == 0 {
		t.Fatal("expected non-empty default Schema-3 library")
	}

	if err := st.SaveLibrary(lib); err != nil {
		t.Fatalf("SaveLibrary failed: %v", err)
	}
	reloaded, err := st.LoadLibrary()
	if err != nil {
		t.Fatalf("Reload LoadLibrary failed: %v", err)
	}
	if reloaded != lib {
		t.Fatal("reloaded DPAPI library did not match saved library")
	}

	prefs := DefaultPreferences()
	prefs.SOCKSPort = 6181
	prefs.IngressMode = IngressLocalSOCKS
	prefs.Appearance = "dark"
	if err := st.SavePreferences(prefs); err != nil {
		t.Fatalf("SavePreferences failed: %v", err)
	}
	got := st.LoadPreferences()
	if got.SOCKSPort != 6181 || got.IngressMode != IngressLocalSOCKS || got.Appearance != "dark" {
		t.Fatalf("unexpected preferences: %+v", got)
	}
}
