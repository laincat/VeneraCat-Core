package offline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/host"
)

type fakeTransport struct {
	mu      sync.Mutex
	routes  map[string][]byte
	status  map[string]int
	calls   map[string]int
	headers map[string]http.Header
	onDo    func(ctx context.Context, req *host.Request) (*host.Response, error)
}

func (f *fakeTransport) Do(ctx context.Context, req *host.Request) (*host.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.onDo != nil {
		if resp, err := f.onDo(ctx, req); resp != nil || err != nil {
			return resp, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[req.URL]++
	if f.headers == nil {
		f.headers = map[string]http.Header{}
	}
	f.headers[req.URL] = req.Header.Clone()

	body, ok := f.routes[req.URL]
	if !ok {
		return &host.Response{Status: http.StatusNotFound, Header: http.Header{}, FinalURL: req.URL}, nil
	}
	st := http.StatusOK
	if code, exists := f.status[req.URL]; exists {
		st = code
	}
	if rng := req.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
		spec := strings.TrimPrefix(rng, "bytes=")
		startStr, endStr, _ := strings.Cut(spec, "-")
		start, _ := strconv.Atoi(startStr)
		end := len(body) - 1
		if endStr != "" {
			if e, err := strconv.Atoi(endStr); err == nil && e < end {
				end = e
			}
		}
		if start < 0 || start >= len(body) || end < start {
			return &host.Response{Status: http.StatusRequestedRangeNotSatisfiable, Header: http.Header{}, FinalURL: req.URL}, nil
		}
		slice := append([]byte(nil), body[start:end+1]...)
		hdr := http.Header{
			"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))},
		}
		return &host.Response{Status: http.StatusPartialContent, Header: hdr, Body: slice, FinalURL: req.URL}, nil
	}
	return &host.Response{Status: st, Header: http.Header{}, Body: append([]byte(nil), body...), FinalURL: req.URL}, nil
}

func (f *fakeTransport) count(u string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[u]
}

func makeTSPayload(prefix []byte, packets int) []byte {
	var b bytes.Buffer
	b.Write(prefix)
	for i := 0; i < packets; i++ {
		pkt := make([]byte, 188)
		pkt[0] = 0x47
		pkt[1] = byte(i + 1)
		b.Write(pkt)
	}
	return b.Bytes()
}

func TestHLSPlanDownloadResumeVerifyAndRewrite(t *testing.T) {
	root := t.TempDir()
	master := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
low/media.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720
hd/media.m3u8
`
	media := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-KEY:METHOD=AES-128,URI="enc.key"
#EXT-X-MAP:URI="init.mp4"
#EXTINF:5.0,
seg0.ts
#EXTINF:5.0,
seg1.ts
#EXT-X-CUE-OUT:2.0
#EXTINF:2.0,
https://ads.example.com/ad0.ts
#EXT-X-CUE-IN
#EXTINF:5.0,
seg2.ts
#EXT-X-ENDLIST
`
	seg0 := makeTSPayload([]byte("PNGFAKEHDR"), 6)
	seg1 := makeTSPayload(nil, 6)
	seg2 := makeTSPayload(nil, 6)
	tr := &fakeTransport{
		routes: map[string][]byte{
			"https://cdn.example.com/master.m3u8":   []byte(master),
			"https://cdn.example.com/hd/media.m3u8": []byte(media),
			"https://cdn.example.com/hd/enc.key":    []byte("0123456789abcdef"),
			"https://cdn.example.com/hd/init.mp4":   []byte("ftypinit"),
			"https://cdn.example.com/hd/seg0.ts":    seg0,
			"https://cdn.example.com/hd/seg1.ts":    seg1,
			"https://cdn.example.com/hd/seg2.ts":    seg2,
			"https://ads.example.com/ad0.ts":        []byte("ad-bytes"),
		},
	}

	req := TaskRequest{
		RootDir:     root,
		TaskID:      "ep_01",
		URL:         "https://cdn.example.com/master.m3u8",
		Kind:        KindAuto,
		Headers:     http.Header{"Referer": {"https://player.example.com/"}},
		Partition:   "video:demo",
		DropAds:     true,
		StripPrefix: true,
		MaxHeight:   720,
	}

	plan, err := PlanTask(context.Background(), tr, req)
	if err != nil {
		t.Fatalf("PlanTask: %v", err)
	}
	if plan.Kind != KindHLS || plan.DroppedAds != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	// 1 key + 1 map + 3 main segments (ad segment must be dropped and never planned/fetched).
	if len(plan.Items) != 5 {
		t.Fatalf("planned items = %d, want 5", len(plan.Items))
	}
	if strings.Contains(plan.RewrittenM3U8, "ads.example.com") {
		t.Fatalf("ad URL leaked into rewritten m3u8:\n%s", plan.RewrittenM3U8)
	}
	if !strings.Contains(plan.RewrittenM3U8, `URI="keys/k0000.key"`) || !strings.Contains(plan.RewrittenM3U8, "segments/s00000.ts") {
		t.Fatalf("unexpected rewritten m3u8:\n%s", plan.RewrittenM3U8)
	}

	// Simulate cancellation mid-download after seg0 succeeds.
	ctx, cancel := context.WithCancel(context.Background())
	tr.onDo = func(_ context.Context, r *host.Request) (*host.Response, error) {
		if r.URL == "https://cdn.example.com/hd/seg1.ts" {
			cancel()
			return nil, context.Canceled
		}
		return nil, nil
	}
	partial, err := Download(ctx, tr, req, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if partial == nil || partial.Status != StatusCanceled || partial.CompletedItems != 3 {
		t.Fatalf("partial result = %+v", partial)
	}
	// Ensure no temporary files remain after cancellation.
	assertNoTempFiles(t, partial.TaskDir)

	// Resume download without cancellation: completed items must be skipped (not re-fetched).
	tr.onDo = nil
	seg0Before := tr.count("https://cdn.example.com/hd/seg0.ts")
	var progressCount int
	res, err := Download(context.Background(), tr, req, func(Progress) {
		progressCount++
	})
	if err != nil {
		t.Fatalf("resume Download: %v", err)
	}
	if res.Status != StatusCompleted || res.SkippedItems != 3 || res.CompletedItems != 5 {
		t.Fatalf("resume result = %+v", res)
	}
	if tr.count("https://cdn.example.com/hd/seg0.ts") != seg0Before {
		t.Fatalf("seg0 was re-downloaded on resume")
	}
	if tr.count("https://ads.example.com/ad0.ts") != 0 {
		t.Fatalf("dropped ad segment should never be fetched")
	}
	if progressCount == 0 {
		t.Fatalf("expected progress callbacks")
	}

	// Verify StripPrefix stripped the fake PNG header from seg0.ts on disk.
	seg0Disk, err := os.ReadFile(filepath.Join(res.TaskDir, "segments", "s00000.ts"))
	if err != nil {
		t.Fatalf("read seg0: %v", err)
	}
	if len(seg0Disk) == 0 || seg0Disk[0] != 0x47 {
		t.Fatalf("StripPrefix failed; first byte = 0x%x", seg0Disk[0])
	}

	// Verify integrity passes cleanly.
	vr, err := Verify(root, "ep_01")
	if err != nil || !vr.Valid || vr.CheckedItems != 5 {
		t.Fatalf("Verify = %+v, err=%v", vr, err)
	}

	// Tampering a segment must fail Verify.
	if err := os.WriteFile(filepath.Join(res.TaskDir, "segments", "s00001.ts"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	vrBad, err := Verify(root, "ep_01")
	if err != nil || vrBad.Valid || len(vrBad.Corrupted) != 1 {
		t.Fatalf("expected corruption detection, got %+v err=%v", vrBad, err)
	}
}

func TestDirectMediaRangeResumeAndCleanup(t *testing.T) {
	root := t.TempDir()
	fullBody := bytes.Repeat([]byte("0123456789abcdef"), 64) // 1024 bytes
	wantHash := bytesSHA256(fullBody)

	tr := &fakeTransport{
		routes: map[string][]byte{
			"https://cdn.example.com/movie.mp4": fullBody,
		},
	}

	// Pre-populate a partial .part file to test HTTP 206 resume.
	taskDir, err := SafeTaskDir(root, "movie_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "media.mp4.part"), fullBody[:400], 0o644); err != nil {
		t.Fatal(err)
	}

	req := TaskRequest{
		RootDir:        root,
		TaskID:         "movie_1",
		URL:            "https://cdn.example.com/movie.mp4",
		ExpectedBytes:  int64(len(fullBody)),
		ExpectedSHA256: wantHash,
	}

	res, err := Download(context.Background(), tr, req, nil)
	if err != nil {
		t.Fatalf("Download direct resume: %v", err)
	}
	if res.DownloadedBytes != int64(len(fullBody)-400) || res.TotalBytes != int64(len(fullBody)) {
		t.Fatalf("direct resume stats = %+v", res)
	}
	if got := tr.headers["https://cdn.example.com/movie.mp4"].Get("Range"); got != "bytes=400-" {
		t.Fatalf("Range header = %q, want bytes=400-", got)
	}

	vr, err := Verify(root, "movie_1")
	if err != nil || !vr.Valid {
		t.Fatalf("Verify direct = %+v err=%v", vr, err)
	}

	// List and Remove.
	list, err := List(root)
	if err != nil || len(list) != 1 || list[0].TaskID != "movie_1" {
		t.Fatalf("List = %+v err=%v", list, err)
	}
	freed, err := Remove(root, "movie_1")
	if err != nil || freed <= 0 {
		t.Fatalf("Remove = %d, %v", freed, err)
	}
	if _, err := Verify(root, "movie_1"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Verify after Remove = %v, want ErrTaskNotFound", err)
	}
}

func TestPathTraversalProtectionAndPrune(t *testing.T) {
	root := t.TempDir()
	badIDs := []string{"", ".", "..", "../escape", `..\escape`, "a/b", `a\b`, "CON", "nul.txt", "bad.", "bad ", "a:b", "a\x00b"}
	for _, id := range badIDs {
		if _, err := SafeTaskDir(root, id); err == nil {
			t.Fatalf("SafeTaskDir accepted unsafe id %q", id)
		}
	}
	badRels := []string{"", "/", "../x", "a/../../x", `a\b`, "c:x", "a/\x00b", "CON/x.ts"}
	for _, rel := range badRels {
		if _, err := SafeFilePath(root, rel); err == nil {
			t.Fatalf("SafeFilePath accepted unsafe rel %q", rel)
		}
	}

	// Create two completed tasks and one incomplete task, then Prune.
	tr := &fakeTransport{
		routes: map[string][]byte{
			"https://cdn.example.com/a.mp4": bytes.Repeat([]byte("a"), 512),
			"https://cdn.example.com/b.mp4": bytes.Repeat([]byte("b"), 512),
		},
	}
	if _, err := Download(context.Background(), tr, TaskRequest{RootDir: root, TaskID: "task_a", URL: "https://cdn.example.com/a.mp4"}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	if _, err := Download(context.Background(), tr, TaskRequest{RootDir: root, TaskID: "task_b", URL: "https://cdn.example.com/b.mp4"}, nil); err != nil {
		t.Fatal(err)
	}
	// Failed/incomplete task_c.
	_, _ = Download(context.Background(), tr, TaskRequest{RootDir: root, TaskID: "task_c", URL: "https://cdn.example.com/missing.mp4"}, nil)
	// Orphan temp file.
	if err := os.WriteFile(filepath.Join(root, "task_b", "orphan.tmp.1"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Evict incomplete + enforce MaxBytes smaller than two tasks combined.
	pr, err := Prune(root, PruneOptions{RemoveIncomplete: true, MaxBytes: 1200})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pr.RemovedTasks) < 2 {
		t.Fatalf("expected incomplete task_c and oldest task_a pruned, got %+v", pr)
	}
	remaining, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].TaskID != "task_b" {
		t.Fatalf("remaining = %+v, want only task_b", remaining)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(d.Name(), ".tmp.") || strings.HasSuffix(d.Name(), ".tmp") {
			t.Fatalf("lingering temp file: %s", p)
		}
		return nil
	})
}
