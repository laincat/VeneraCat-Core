package gohost

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// A source's declared settings reach the client as JSON, with values that are
// not strings left alone: a source may declare a number or a boolean, and the
// client must store what the source wrote.
func TestSettingsDeclaration(t *testing.T) {
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	src := `class S extends ComicSource {
	  name='S'; key='s'; version='1'
	  settings = {
	    region: {title:'CDN线路', type:'select',
	      options:[{value:'1', text:'大陆线路'}, {value:'0', text:'海外线路'}], default:'1'},
	    autoLogin: {title:'自动登录', type:'switch', default:true},
	    limits: {title:'上限', type:'input', default:20},
	  }
	}`
	if _, err := c.Install(context.Background(), src, "", ""); err != nil {
		t.Fatalf("install: %v", err)
	}

	list, err := c.Settings(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("settings = %+v", list)
	}
	region := list[0]
	if region.Key != "region" || region.Title != "CDN线路" || region.Type != "select" {
		t.Errorf("region = %+v", region)
	}
	if string(region.Default) != `"1"` {
		t.Errorf("region default = %s", region.Default)
	}
	if len(region.Options) != 2 || string(region.Options[1].Value) != `"0"` || region.Options[1].Text != "海外线路" {
		t.Errorf("region options = %+v", region.Options)
	}
	// Numbers and booleans stay as they were written.
	if string(list[1].Default) != "true" {
		t.Errorf("switch default = %s", list[1].Default)
	}
	if string(list[2].Default) != "20" {
		t.Errorf("input default = %s", list[2].Default)
	}
}

// A chosen value is stored as JSON and read back through the same host call
// the source uses.
func TestSettingsRoundTrip(t *testing.T) {
	c, err := Open(Options{DataDir: filepath.Join(t.TempDir(), "data"), AppVersion: "1.6.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	raw := json.RawMessage(`"Compressed"`)
	if err := c.Store().SaveSetting("manga_dex", "image_quality", raw); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Store().LoadSetting("manga_dex", "image_quality")
	if err != nil || !ok {
		t.Fatalf("load = %q, %v, %v", got, ok, err)
	}
	if string(got) != string(raw) {
		t.Errorf("stored %s, want %s", got, raw)
	}
	// A number keeps its digits, not a float's.
	big := json.RawMessage(`12345678901234567890`)
	if err := c.Store().SaveSetting("manga_dex", "limit", big); err != nil {
		t.Fatal(err)
	}
	if got, _, err := c.Store().LoadSetting("manga_dex", "limit"); err != nil || string(got) != string(big) {
		t.Errorf("big number = %s, %v", got, err)
	}
}
