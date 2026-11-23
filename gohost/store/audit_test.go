package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditNullObjectsRemainWritable(t *testing.T) {
	s := open(t)
	p := filepath.Join(s.Root(), "sources", "k.json")
	if err := os.WriteFile(p, []byte(`{"data":null,"settings":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveData("k", "v", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSetting("k", "v", json.RawMessage(`2`)); err != nil {
		t.Fatal(err)
	}
}

func TestAuditFailedWriteDoesNotChangeCachedState(t *testing.T) {
	s := open(t)
	if err := s.SaveData("k", "v", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), "sources", "k.json.tmp")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveData("k", "v", json.RawMessage(`2`)); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	got, _, err := s.LoadData("k", "v")
	if err != nil || string(got) != "1" {
		t.Fatalf("failed write changed cache: %s, %v", got, err)
	}
	if err := s.DeleteData("k", "v"); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	got, ok, err := s.LoadData("k", "v")
	if err != nil || !ok || string(got) != "1" {
		t.Fatalf("failed delete changed cache: %s, %v", got, err)
	}
}

func TestAuditCorruptedSourceIsNotSilentlyOverwritten(t *testing.T) {
	s := open(t)
	p := filepath.Join(s.Root(), "sources", "k.json")
	for _, bad := range []string{`{"data":`, `null`, `[]`, `{"data":[]}`, `{"settings":1}`} {
		if err := os.WriteFile(p, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveData("k", "v", json.RawMessage(`1`)); err == nil {
			t.Errorf("silently replaced invalid source data: %s", bad)
		}
		got, err := os.ReadFile(p)
		if err != nil || string(got) != bad {
			t.Errorf("original source file changed: %s, %v", got, err)
		}
	}
}
