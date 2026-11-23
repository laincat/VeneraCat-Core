package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDataRoundTrips(t *testing.T) {
	s := open(t)
	big := json.RawMessage(`{"n":12345678901234567890,"z":1,"a":2}`)
	if err := s.SaveData("wnacg", "last", big); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LoadData("wnacg", "last")
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if string(got) != string(big) {
		t.Fatalf("value = %s, want %s", got, big)
	}
	// A second store reads the file written by the first.
	s2, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	again, ok, _ := s2.LoadData("wnacg", "last")
	if !ok || string(again) != string(big) {
		t.Fatalf("reloaded = %s", again)
	}
	if err := s2.DeleteData("wnacg", "last"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s2.LoadData("wnacg", "last"); ok {
		t.Fatal("deleted value still present")
	}
}

func TestSettingsAreSeparateFromData(t *testing.T) {
	s := open(t)
	_ = s.SaveData("k", "x", json.RawMessage(`1`))
	_ = s.SaveSetting("k", "x", json.RawMessage(`2`))
	if v, _, _ := s.LoadData("k", "x"); string(v) != "1" {
		t.Errorf("data = %s", v)
	}
	if v, _, _ := s.LoadSetting("k", "x"); string(v) != "2" {
		t.Errorf("setting = %s", v)
	}
}

func TestLoggedInMeansAccount(t *testing.T) {
	s := open(t)
	if s.IsLogged("a") {
		t.Fatal("new source is logged in")
	}
	_ = s.SaveData("a", "account", json.RawMessage(`["u","p"]`))
	if !s.IsLogged("a") {
		t.Fatal("account did not count as logged in")
	}
	_ = s.SaveData("a", "account", json.RawMessage(`null`))
	if s.IsLogged("a") {
		t.Fatal("null account counted as logged in")
	}
}

func TestRejectsBadKeysAndValues(t *testing.T) {
	s := open(t)
	for _, key := range []string{"", "../x", "a/b", "a.b"} {
		if err := s.SaveData(key, "k", json.RawMessage(`1`)); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
	if err := s.SaveData("ok", "k", json.RawMessage(`{`)); err == nil {
		t.Error("invalid JSON accepted")
	}
	if err := s.SaveData("ok", "k", nil); err == nil {
		t.Error("empty value accepted")
	}
}
