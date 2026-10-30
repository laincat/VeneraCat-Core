package subscription

import (
	"strings"
	"testing"
)

const good = `{
  "$schema": "vcvideo/1",
  "name": "示例视频订阅",
  "plugins": [
    {"type": "kazumi-rule", "key": "kz_AGE", "name": "AGE", "version": "1.6",
     "url": "rules/AGE.json"},
    {"type": "js", "key": "demo_anime", "name": "示例番剧站", "version": "0.1.0",
     "url": "https://example.test/demo.js",
     "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
  ]
}`

func TestParseResolvesRelativeURLs(t *testing.T) {
	idx, err := Parse([]byte(good), "https://raw.example.test/owner/repo/main/vcvideo.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Plugins[0].URL; got != "https://raw.example.test/owner/repo/main/rules/AGE.json" {
		t.Fatalf("relative url = %s", got)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"wrong schema":   strings.Replace(good, "vcvideo/1", "other", 1),
		"bad key":        strings.Replace(good, `"kz_AGE"`, `"kz-AGE"`, 1),
		"duplicate key":  strings.Replace(good, `"demo_anime"`, `"kz_AGE"`, 1),
		"unknown type":   strings.Replace(good, `"type": "js"`, `"type": "exe"`, 1),
		"bad sha":        strings.Replace(good, "0123456789abcdef0123", "0123456789ABCDEF0123", 1),
		"file scheme":    strings.Replace(good, "https://example.test/demo.js", "file:///etc/passwd", 1),
		"url credential": strings.Replace(good, "https://example.test/demo.js", "https://u:p@example.test/x.js", 1),
		"comic array":    `[{"name":"x","key":"x","version":"1","url":"x.js"}]`,
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc), "https://raw.example.test/vcvideo.json"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
