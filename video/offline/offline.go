package offline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video/hls"
)

// PlanTask inspects req.URL via transport (without writing media files to disk)
// and returns a deterministic offline download plan. For HLS master playlists
// it resolves and fetches the chosen media playlist and rewrites it to local
// relative paths.
func PlanTask(ctx context.Context, transport host.Transport, req TaskRequest) (*Plan, error) {
	if err := validateRequest(transport, req, false); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kind := resolveKind(req.Kind, req.URL)
	if kind == KindDirect {
		ext := safeMediaExt(req.URL)
		entry := "media" + ext
		return &Plan{
			TaskID:    req.TaskID,
			Kind:      KindDirect,
			SourceURL: req.URL,
			MediaURL:  req.URL,
			EntryPath: entry,
			Items: []PlannedItem{
				{
					Kind:      ItemMedia,
					Index:     0,
					URL:       req.URL,
					LocalPath: entry,
				},
			},
		}, nil
	}
	return planHLS(ctx, transport, req)
}

func planHLS(ctx context.Context, transport host.Transport, req TaskRequest) (*Plan, error) {
	curURL := req.URL
	var (
		mediaBody []byte
		warnings  []string
	)
	for depth := 0; depth < 3; depth++ {
		resp, err := fetchBytes(ctx, transport, curURL, req.Headers, req.Cookies, req.Partition, "", DefaultMaxPlaylistBytes)
		if err != nil {
			return nil, fmt.Errorf("offline: fetch playlist %q: %w", curURL, err)
		}
		if resp.FinalURL != "" && host.ValidateURL(resp.FinalURL) == nil {
			curURL = resp.FinalURL
		}
		pl, err := hls.Parse(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("offline: parse playlist %q: %w", curURL, err)
		}
		if !pl.Master {
			mediaBody = resp.Body
			break
		}
		chooser := hls.VariantChooser{
			Mode:   hls.VariantHighestBandwidth,
			Output: hls.OutputReference,
		}
		if req.MaxHeight > 0 {
			chooser.Mode = hls.VariantMaxHeight
			chooser.MaxHeight = req.MaxHeight
		}
		rw, err := hls.RewriteDetailed(resp.Body, hls.RewriteOptions{
			BaseURL:       curURL,
			SelectVariant: chooser,
		})
		if err != nil {
			return nil, fmt.Errorf("offline: select HLS variant: %w", err)
		}
		warnings = append(warnings, rw.Warnings...)
		nextURL := strings.TrimSpace(rw.SelectedURL)
		if nextURL == "" {
			nextURL = strings.TrimSpace(string(rw.Body))
		}
		if err := host.ValidateURL(nextURL); err != nil {
			return nil, fmt.Errorf("offline: invalid variant URL %q: %w", nextURL, err)
		}
		curURL = nextURL
	}
	if len(mediaBody) == 0 {
		return nil, errors.New("offline: HLS master playlist redirect depth exceeded")
	}

	parsedMedia, err := hls.Parse(mediaBody)
	if err != nil {
		return nil, fmt.Errorf("offline: parse media playlist: %w", err)
	}

	var (
		items       []PlannedItem
		keyLocal    = map[string]string{}
		mapLocal    = map[string]string{}
		segCount    = 0
		keyCount    = 0
		mapCount    = 0
		mapErr      error
		segByAbs    = map[string][]hls.Segment{}
		segCursor   = map[string]int{}
		maxSegments = req.MaxSegments
	)
	if maxSegments <= 0 {
		maxSegments = DefaultMaxSegments
	}
	for _, s := range parsedMedia.Segments {
		if abs, rerr := hls.Resolve(curURL, s.URI); rerr == nil {
			segByAbs[abs] = append(segByAbs[abs], s)
		}
	}

	rw, err := hls.RewriteDetailed(mediaBody, hls.RewriteOptions{
		BaseURL:     curURL,
		DropAds:     req.DropAds,
		StripPrefix: req.StripPrefix,
		MapURL: func(abs string, kind hls.URIKind) string {
			if err := host.ValidateURL(abs); err != nil {
				if mapErr == nil {
					mapErr = fmt.Errorf("offline: invalid %s URL %q: %w", kind, abs, err)
				}
				return ""
			}
			switch kind {
			case hls.KindKey:
				if lp, ok := keyLocal[abs]; ok {
					return lp
				}
				lp := fmt.Sprintf("keys/k%04d.key", keyCount)
				keyCount++
				keyLocal[abs] = lp
				items = append(items, PlannedItem{
					Kind:      ItemKey,
					Index:     len(items),
					URL:       abs,
					LocalPath: lp,
				})
				return lp
			case hls.KindMap:
				if lp, ok := mapLocal[abs]; ok {
					return lp
				}
				ext := safeSegmentExt(abs, ".mp4")
				lp := fmt.Sprintf("maps/m%04d%s", mapCount, ext)
				mapCount++
				mapLocal[abs] = lp
				items = append(items, PlannedItem{
					Kind:      ItemMap,
					Index:     len(items),
					URL:       abs,
					LocalPath: lp,
				})
				return lp
			case hls.KindSegment:
				if segCount >= maxSegments {
					if mapErr == nil {
						mapErr = fmt.Errorf("offline: HLS segment count exceeds limit %d", maxSegments)
					}
					return ""
				}
				ext := safeSegmentExt(abs, ".ts")
				lp := fmt.Sprintf("segments/s%05d%s", segCount, ext)
				var seq int64
				var dur float64
				var byteRange string
				if list := segByAbs[abs]; segCursor[abs] < len(list) {
					s := list[segCursor[abs]]
					segCursor[abs]++
					seq = s.Sequence
					dur = s.Duration
					byteRange = s.ByteRange
				}
				segCount++
				items = append(items, PlannedItem{
					Kind:      ItemSegment,
					Index:     len(items),
					Sequence:  seq,
					URL:       abs,
					LocalPath: lp,
					ByteRange: byteRange,
					Duration:  dur,
				})
				return lp
			default:
				return abs
			}
		},
	})
	if mapErr != nil {
		return nil, mapErr
	}
	if err != nil {
		return nil, fmt.Errorf("offline: rewrite HLS playlist: %w", err)
	}
	if segCount == 0 {
		return nil, errors.New("offline: HLS media playlist has no segments")
	}
	warnings = append(warnings, rw.Warnings...)

	var totalDur float64
	for _, it := range items {
		if it.Kind == ItemSegment {
			totalDur += it.Duration
		}
	}

	return &Plan{
		TaskID:        req.TaskID,
		Kind:          KindHLS,
		SourceURL:     req.URL,
		MediaURL:      curURL,
		EntryPath:     DefaultPlaylistName,
		Items:         items,
		TotalDuration: totalDur,
		DroppedAds:    len(rw.DroppedGroups),
		Warnings:      warnings,
		RewrittenM3U8: string(rw.Body),
	}, nil
}

// Download executes or resumes an offline cache task, fetching every remote
// resource via transport and writing verified assets into <RootDir>/<TaskID>.
// On cancellation or error, active handles are closed and temporary files are
// removed cleanly while preserving already-verified items for resume.
func Download(ctx context.Context, transport host.Transport, req TaskRequest, onProgress func(Progress)) (*Result, error) {
	if err := validateRequest(transport, req, true); err != nil {
		return nil, err
	}
	taskDir, err := SafeTaskDir(req.RootDir, req.TaskID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return nil, fmt.Errorf("offline: create task directory: %w", err)
	}
	// Always sweep orphaned temp files on exit.
	defer removeTempFiles(taskDir)

	manifest, err := loadOrInitManifest(ctx, transport, taskDir, req)
	if err != nil {
		return nil, err
	}
	manifest.Status = StatusDownloading
	manifest.LastError = ""
	manifest.UpdatedAt = time.Now().UnixMilli()
	if err := saveManifest(taskDir, manifest); err != nil {
		return nil, err
	}

	var (
		res    *Result
		runErr error
	)
	if manifest.Kind == KindDirect {
		res, runErr = downloadDirect(ctx, transport, taskDir, manifest, req, onProgress)
	} else {
		res, runErr = downloadHLS(ctx, transport, taskDir, manifest, req, onProgress)
	}

	manifest.UpdatedAt = time.Now().UnixMilli()
	if runErr != nil {
		if ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			manifest.Status = StatusCanceled
		} else {
			manifest.Status = StatusFailed
		}
		manifest.LastError = runErr.Error()
		_ = saveManifest(taskDir, manifest)
		if res != nil {
			res.Status = manifest.Status
		}
		return res, runErr
	}

	manifest.Status = StatusCompleted
	manifest.LastError = ""
	if err := saveManifest(taskDir, manifest); err != nil {
		return res, err
	}
	res.Status = StatusCompleted
	if onProgress != nil {
		onProgress(Progress{
			TaskID:          manifest.TaskID,
			Status:          StatusCompleted,
			CompletedItems:  res.CompletedItems,
			TotalItems:      res.TotalItems,
			DownloadedBytes: res.DownloadedBytes,
			TotalBytes:      res.TotalBytes,
		})
	}
	return res, nil
}

func loadOrInitManifest(ctx context.Context, transport host.Transport, taskDir string, req TaskRequest) (*Manifest, error) {
	if existing, err := readManifest(taskDir); err == nil {
		wantKind := resolveKind(req.Kind, req.URL)
		if existing.SourceURL == req.URL && existing.TaskID == req.TaskID && (req.Kind == "" || req.Kind == KindAuto || existing.Kind == wantKind) && len(existing.Items) > 0 {
			return existing, nil
		}
		// Source URL or kind changed for this task ID: reset directory cleanly.
		if err := clearDirContents(taskDir); err != nil {
			return nil, fmt.Errorf("offline: reset task directory: %w", err)
		}
	}
	plan, err := PlanTask(ctx, transport, req)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	items := make([]ManifestItem, len(plan.Items))
	for i, it := range plan.Items {
		items[i] = ManifestItem{
			Kind:      it.Kind,
			Index:     it.Index,
			Sequence:  it.Sequence,
			URL:       it.URL,
			LocalPath: it.LocalPath,
			ByteRange: it.ByteRange,
			Duration:  it.Duration,
		}
	}
	return &Manifest{
		Version:       ManifestVersion,
		TaskID:        plan.TaskID,
		Kind:          plan.Kind,
		Status:        StatusPlanned,
		SourceURL:     plan.SourceURL,
		MediaURL:      plan.MediaURL,
		EntryPath:     plan.EntryPath,
		RewrittenM3U8: plan.RewrittenM3U8,
		TotalDuration: plan.TotalDuration,
		DroppedAds:    plan.DroppedAds,
		Warnings:      append([]string(nil), plan.Warnings...),
		Items:         items,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func downloadHLS(ctx context.Context, transport host.Transport, taskDir string, m *Manifest, req TaskRequest, onProgress func(Progress)) (*Result, error) {
	maxSeg := req.MaxSegmentBytes
	if maxSeg <= 0 {
		maxSeg = DefaultMaxSegmentBytes
	}
	workers := req.Concurrency
	if workers < 1 {
		workers = 1
	}
	if workers > 4 {
		workers = 4
	}

	entryFile, err := SafeFilePath(taskDir, m.EntryPath)
	if err != nil {
		return nil, err
	}

	var (
		mu              sync.Mutex
		completedItems  int
		skippedItems    int
		downloadedBytes int64
		totalBytes      int64
		pending         []int
	)

	// Verify already-completed items on disk to support safe resume.
	for i := range m.Items {
		it := &m.Items[i]
		dest, err := SafeFilePath(taskDir, it.LocalPath)
		if err != nil {
			return nil, err
		}
		if it.Completed && it.SHA256 != "" {
			if sum, sz, err := fileSHA256AndSize(dest); err == nil && sum == it.SHA256 && sz == it.Bytes {
				completedItems++
				skippedItems++
				totalBytes += sz
				continue
			}
			it.Completed = false
			it.SHA256 = ""
			it.Bytes = 0
		}
		pending = append(pending, i)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		firstErr error
		errOnce  sync.Once
		setErr   = func(e error) {
			errOnce.Do(func() {
				firstErr = e
				cancel()
			})
		}
	)

	if len(pending) > 0 {
		jobs := make(chan int, len(pending))
		for _, idx := range pending {
			jobs <- idx
		}
		close(jobs)

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range jobs {
					if err := runCtx.Err(); err != nil {
						setErr(err)
						return
					}
					it := m.Items[idx]
					dest, err := SafeFilePath(taskDir, it.LocalPath)
					if err != nil {
						setErr(err)
						return
					}
					rangeHeader, err := hlsByteRangeHeader(it.ByteRange)
					if err != nil {
						setErr(err)
						return
					}
					resp, err := fetchBytes(runCtx, transport, it.URL, req.Headers, req.Cookies, req.Partition, rangeHeader, maxSeg)
					if err != nil {
						setErr(fmt.Errorf("offline: download %s %q: %w", it.Kind, it.LocalPath, err))
						return
					}
					body := resp.Body
					if req.StripPrefix && it.Kind == ItemSegment {
						body = hls.StripSegmentPrefix(body)
					}

					mu.Lock()
					if req.MaxTotalBytes > 0 && totalBytes+int64(len(body)) > req.MaxTotalBytes {
						mu.Unlock()
						setErr(fmt.Errorf("offline: cache size exceeds maxTotalBytes (%d)", req.MaxTotalBytes))
						return
					}
					mu.Unlock()

					if err := atomicWriteFile(dest, body); err != nil {
						setErr(fmt.Errorf("offline: write %s: %w", it.LocalPath, err))
						return
					}
					sum := bytesSHA256(body)
					sz := int64(len(body))

					mu.Lock()
					m.Items[idx].Completed = true
					m.Items[idx].Bytes = sz
					m.Items[idx].SHA256 = sum
					m.Items[idx].UpdatedAt = time.Now().UnixMilli()
					completedItems++
					downloadedBytes += sz
					totalBytes += sz
					m.UpdatedAt = time.Now().UnixMilli()
					_ = saveManifest(taskDir, m)
					prog := Progress{
						TaskID:          m.TaskID,
						Status:          StatusDownloading,
						CompletedItems:  completedItems,
						TotalItems:      len(m.Items),
						DownloadedBytes: downloadedBytes,
						TotalBytes:      totalBytes,
						CurrentItem:     it.LocalPath,
					}
					mu.Unlock()

					if onProgress != nil {
						onProgress(prog)
					}
				}
			}()
		}
		wg.Wait()
	}

	res := &Result{
		TaskID:          m.TaskID,
		Kind:            m.Kind,
		Status:          StatusDownloading,
		TaskDir:         taskDir,
		EntryPath:       m.EntryPath,
		EntryFile:       entryFile,
		CompletedItems:  completedItems,
		TotalItems:      len(m.Items),
		SkippedItems:    skippedItems,
		DownloadedBytes: downloadedBytes,
		TotalBytes:      totalBytes,
		Warnings:        append([]string(nil), m.Warnings...),
	}
	if firstErr != nil {
		return res, firstErr
	}

	// Write the local rewritten M3U8 entry playlist atomically.
	playlistBytes := []byte(m.RewrittenM3U8)
	if err := atomicWriteFile(entryFile, playlistBytes); err != nil {
		return res, fmt.Errorf("offline: write local playlist: %w", err)
	}
	m.EntryBytes = int64(len(playlistBytes))
	m.EntrySHA256 = bytesSHA256(playlistBytes)
	return res, nil
}

func downloadDirect(ctx context.Context, transport host.Transport, taskDir string, m *Manifest, req TaskRequest, onProgress func(Progress)) (*Result, error) {
	if len(m.Items) == 0 {
		return nil, errors.New("offline: direct task has no planned media item")
	}
	it := &m.Items[0]
	dest, err := SafeFilePath(taskDir, it.LocalPath)
	if err != nil {
		return nil, err
	}
	partPath := dest + ".part"

	// Fast path: already downloaded and verified.
	if it.Completed && it.SHA256 != "" {
		if sum, sz, err := fileSHA256AndSize(dest); err == nil && sum == it.SHA256 && sz == it.Bytes {
			if (req.ExpectedSHA256 == "" || strings.EqualFold(req.ExpectedSHA256, sum)) &&
				(req.ExpectedBytes <= 0 || req.ExpectedBytes == sz) {
				m.EntryBytes = sz
				m.EntrySHA256 = sum
				return &Result{
					TaskID:         m.TaskID,
					Kind:           KindDirect,
					Status:         StatusCompleted,
					TaskDir:        taskDir,
					EntryPath:      m.EntryPath,
					EntryFile:      dest,
					CompletedItems: 1,
					TotalItems:     1,
					SkippedItems:   1,
					TotalBytes:     sz,
				}, nil
			}
		}
		it.Completed = false
		it.SHA256 = ""
		it.Bytes = 0
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var existingOffset int64
	if fi, err := os.Stat(partPath); err == nil && !fi.IsDir() {
		existingOffset = fi.Size()
	}

	rangeHdr := ""
	if existingOffset > 0 {
		rangeHdr = fmt.Sprintf("bytes=%d-", existingOffset)
	}
	maxBytes := req.MaxTotalBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxSegmentBytes * 16 // 1 GiB upper bound when unspecified
	}

	resp, err := fetchRaw(ctx, transport, it.URL, req.Headers, req.Cookies, req.Partition, rangeHdr)
	if err != nil {
		return &Result{
			TaskID:     m.TaskID,
			Kind:       KindDirect,
			Status:     StatusFailed,
			TaskDir:    taskDir,
			EntryPath:  m.EntryPath,
			EntryFile:  dest,
			TotalItems: 1,
			TotalBytes: existingOffset,
		}, fmt.Errorf("offline: download direct media: %w", err)
	}

	var downloaded int64
	switch {
	case resp.Status == http.StatusPartialContent && existingOffset > 0 && contentRangeMatchesOffset(resp.Header.Get("Content-Range"), existingOffset):
		if existingOffset+int64(len(resp.Body)) > maxBytes {
			return nil, fmt.Errorf("offline: direct media exceeds maxTotalBytes (%d)", maxBytes)
		}
		if err := appendFile(partPath, resp.Body); err != nil {
			return nil, fmt.Errorf("offline: append partial file: %w", err)
		}
		downloaded = int64(len(resp.Body))
	case resp.Status == http.StatusRequestedRangeNotSatisfiable && existingOffset > 0 && req.ExpectedBytes > 0 && existingOffset == req.ExpectedBytes:
		// The .part file already holds all bytes.
		downloaded = 0
	case resp.Status >= 200 && resp.Status < 300:
		if int64(len(resp.Body)) > maxBytes {
			return nil, fmt.Errorf("offline: direct media exceeds maxTotalBytes (%d)", maxBytes)
		}
		if err := atomicWriteFile(partPath, resp.Body); err != nil {
			return nil, fmt.Errorf("offline: write media file: %w", err)
		}
		downloaded = int64(len(resp.Body))
	default:
		return nil, fmt.Errorf("offline: unexpected HTTP status %d for %q", resp.Status, it.URL)
	}

	sum, sz, err := fileSHA256AndSize(partPath)
	if err != nil {
		return nil, fmt.Errorf("offline: hash downloaded file: %w", err)
	}
	if req.ExpectedBytes > 0 && sz != req.ExpectedBytes {
		return nil, fmt.Errorf("%w: size %d does not match expected %d", ErrIntegrityFailed, sz, req.ExpectedBytes)
	}
	if req.ExpectedSHA256 != "" && !strings.EqualFold(sum, strings.TrimSpace(req.ExpectedSHA256)) {
		return nil, fmt.Errorf("%w: sha256 %s does not match expected %s", ErrIntegrityFailed, sum, req.ExpectedSHA256)
	}
	if err := os.Rename(partPath, dest); err != nil {
		return nil, fmt.Errorf("offline: finalize media file: %w", err)
	}

	now := time.Now().UnixMilli()
	it.Completed = true
	it.Bytes = sz
	it.SHA256 = sum
	it.UpdatedAt = now
	m.EntryBytes = sz
	m.EntrySHA256 = sum

	if onProgress != nil {
		onProgress(Progress{
			TaskID:          m.TaskID,
			Status:          StatusDownloading,
			CompletedItems:  1,
			TotalItems:      1,
			DownloadedBytes: downloaded,
			TotalBytes:      sz,
			CurrentItem:     it.LocalPath,
		})
	}

	return &Result{
		TaskID:          m.TaskID,
		Kind:            KindDirect,
		Status:          StatusCompleted,
		TaskDir:         taskDir,
		EntryPath:       m.EntryPath,
		EntryFile:       dest,
		CompletedItems:  1,
		TotalItems:      1,
		DownloadedBytes: downloaded,
		TotalBytes:      sz,
	}, nil
}

// Inspect returns the persisted Manifest of taskID under rootDir.
func Inspect(rootDir, taskID string) (*Manifest, error) {
	taskDir, err := SafeTaskDir(rootDir, taskID)
	if err != nil {
		return nil, err
	}
	m, err := readManifest(taskDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	return m, nil
}

// Verify checks that every planned asset and the entry file of taskID exist on
// disk and match their recorded byte lengths and SHA-256 checksums.
func Verify(rootDir, taskID string) (*VerifyResult, error) {
	taskDir, err := SafeTaskDir(rootDir, taskID)
	if err != nil {
		return nil, err
	}
	m, err := readManifest(taskDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	res := &VerifyResult{
		TaskID: taskID,
		Status: m.Status,
	}
	entryFile, err := SafeFilePath(taskDir, m.EntryPath)
	if err != nil {
		return nil, err
	}
	res.EntryFile = entryFile

	for _, it := range m.Items {
		dest, err := SafeFilePath(taskDir, it.LocalPath)
		if err != nil {
			res.Corrupted = append(res.Corrupted, it.LocalPath)
			continue
		}
		if !it.Completed || it.SHA256 == "" {
			res.Missing = append(res.Missing, it.LocalPath)
			continue
		}
		sum, sz, err := fileSHA256AndSize(dest)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				res.Missing = append(res.Missing, it.LocalPath)
			} else {
				res.Corrupted = append(res.Corrupted, it.LocalPath)
			}
			continue
		}
		if sz != it.Bytes || sum != it.SHA256 {
			res.Corrupted = append(res.Corrupted, it.LocalPath)
			continue
		}
		res.CheckedItems++
		res.TotalBytes += sz
	}

	if m.Kind == KindHLS {
		sum, sz, err := fileSHA256AndSize(entryFile)
		if err != nil {
			res.Missing = append(res.Missing, m.EntryPath)
		} else if (m.EntrySHA256 != "" && sum != m.EntrySHA256) || (m.EntryBytes > 0 && sz != m.EntryBytes) {
			res.Corrupted = append(res.Corrupted, m.EntryPath)
		} else {
			res.TotalBytes += sz
		}
	}

	res.Valid = m.Status == StatusCompleted && len(res.Missing) == 0 && len(res.Corrupted) == 0
	return res, nil
}

// List returns summaries of all valid cached tasks found directly inside rootDir.
func List(rootDir string) ([]Summary, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, errors.New("offline: root directory is required")
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Summary{}, nil
		}
		return nil, err
	}
	out := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || ValidateTaskID(entry.Name()) != nil {
			continue
		}
		taskDir, err := SafeTaskDir(rootDir, entry.Name())
		if err != nil {
			continue
		}
		m, err := readManifest(taskDir)
		if err != nil {
			continue
		}
		entryFile, err := SafeFilePath(taskDir, m.EntryPath)
		if err != nil {
			continue
		}
		done := 0
		for _, it := range m.Items {
			if it.Completed {
				done++
			}
		}
		out = append(out, Summary{
			TaskID:         m.TaskID,
			Kind:           m.Kind,
			Status:         m.Status,
			SourceURL:      m.SourceURL,
			EntryPath:      m.EntryPath,
			EntryFile:      entryFile,
			CompletedItems: done,
			TotalItems:     len(m.Items),
			DiskBytes:      dirSize(taskDir),
			CreatedAt:      m.CreatedAt,
			UpdatedAt:      m.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out, nil
}

// Remove deletes a cached task directory and returns reclaimed disk bytes.
func Remove(rootDir, taskID string) (int64, error) {
	taskDir, err := SafeTaskDir(rootDir, taskID)
	if err != nil {
		return 0, err
	}
	if _, err := os.Lstat(taskDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrTaskNotFound
		}
		return 0, err
	}
	size := dirSize(taskDir)
	if err := os.RemoveAll(taskDir); err != nil {
		return 0, fmt.Errorf("offline: remove task %q: %w", taskID, err)
	}
	return size, nil
}

// Prune removes orphaned temporary files, optionally deletes incomplete tasks,
// and evicts oldest tasks when total disk usage exceeds opts.MaxBytes.
func Prune(rootDir string, opts PruneOptions) (*PruneResult, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, errors.New("offline: root directory is required")
	}
	res := &PruneResult{}
	if _, err := os.Stat(rootDir); errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	res.ReclaimedBytes += removeTempFiles(rootDir)

	summaries, err := List(rootDir)
	if err != nil {
		return nil, err
	}

	kept := make([]Summary, 0, len(summaries))
	var total int64
	for _, s := range summaries {
		if opts.RemoveIncomplete && s.Status != StatusCompleted {
			freed, err := Remove(rootDir, s.TaskID)
			if err == nil {
				res.RemovedTasks = append(res.RemovedTasks, s.TaskID)
				res.ReclaimedBytes += freed
			}
			continue
		}
		kept = append(kept, s)
		total += s.DiskBytes
	}

	if opts.MaxBytes > 0 && total > opts.MaxBytes {
		// Evict oldest UpdatedAt first.
		sort.Slice(kept, func(i, j int) bool {
			if kept[i].UpdatedAt != kept[j].UpdatedAt {
				return kept[i].UpdatedAt < kept[j].UpdatedAt
			}
			return kept[i].TaskID < kept[j].TaskID
		})
		for _, s := range kept {
			if total <= opts.MaxBytes {
				break
			}
			freed, err := Remove(rootDir, s.TaskID)
			if err != nil {
				continue
			}
			res.RemovedTasks = append(res.RemovedTasks, s.TaskID)
			res.ReclaimedBytes += freed
			total -= freed
		}
	}
	if total < 0 {
		total = 0
	}
	res.RemainingBytes = total
	return res, nil
}

// --- helpers ----------------------------------------------------------------

func validateRequest(transport host.Transport, req TaskRequest, needRoot bool) error {
	if transport == nil {
		return errors.New("offline: host transport is required")
	}
	if err := ValidateTaskID(req.TaskID); err != nil {
		return err
	}
	if needRoot && strings.TrimSpace(req.RootDir) == "" {
		return errors.New("offline: root directory is required")
	}
	if err := host.ValidateURL(req.URL); err != nil {
		return fmt.Errorf("offline: invalid url: %w", err)
	}
	for k, vals := range req.Headers {
		for _, v := range vals {
			if err := host.ValidHeader(k, v); err != nil {
				return fmt.Errorf("offline: %w", err)
			}
		}
	}
	switch req.Kind {
	case "", KindAuto, KindHLS, KindDirect:
	default:
		return fmt.Errorf("offline: unsupported kind %q", req.Kind)
	}
	return nil
}

func resolveKind(k Kind, rawURL string) Kind {
	if k == KindHLS || k == KindDirect {
		return k
	}
	u, err := url.Parse(rawURL)
	if err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8") {
		return KindHLS
	}
	return KindDirect
}

func safeMediaExt(rawURL string) string {
	return safeSegmentExt(rawURL, ".mp4")
}

func safeSegmentExt(rawURL, fallback string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fallback
	}
	ext := strings.ToLower(path.Ext(u.Path))
	switch ext {
	case ".ts", ".m4s", ".mp4", ".m4v", ".m4a", ".mkv", ".webm", ".mov", ".flv", ".aac", ".key", ".bin":
		return ext
	default:
		return fallback
	}
}

func fetchRaw(ctx context.Context, transport host.Transport, rawURL string, hdr http.Header, cookies bool, partition, rangeHeader string) (*host.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := host.ValidateURL(rawURL); err != nil {
		return nil, err
	}
	h := http.Header{}
	for k, vals := range hdr {
		for _, v := range vals {
			h.Add(k, v)
		}
	}
	if rangeHeader != "" {
		h.Set("Range", rangeHeader)
	}
	return transport.Do(ctx, &host.Request{
		Method:    http.MethodGet,
		URL:       rawURL,
		Header:    h,
		Cookies:   cookies,
		Partition: partition,
	})
}

func fetchBytes(ctx context.Context, transport host.Transport, rawURL string, hdr http.Header, cookies bool, partition, rangeHeader string, maxBytes int64) (*host.Response, error) {
	resp, err := fetchRaw(ctx, transport, rawURL, hdr, cookies, partition, rangeHeader)
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.Status)
	}
	if maxBytes > 0 && int64(len(resp.Body)) > maxBytes {
		return nil, fmt.Errorf("response body %d exceeds limit %d", len(resp.Body), maxBytes)
	}
	return resp, nil
}

// hlsByteRangeHeader converts an EXT-X-BYTERANGE value ("length@offset") into
// an HTTP Range header when offset is explicit.
func hlsByteRangeHeader(br string) (string, error) {
	br = strings.TrimSpace(br)
	if br == "" {
		return "", nil
	}
	lenStr, offStr, ok := strings.Cut(br, "@")
	if !ok {
		return "", nil
	}
	n, err1 := strconv.ParseInt(strings.TrimSpace(lenStr), 10, 64)
	off, err2 := strconv.ParseInt(strings.TrimSpace(offStr), 10, 64)
	if err1 != nil || err2 != nil || n <= 0 || off < 0 {
		return "", fmt.Errorf("offline: invalid HLS byte range %q", br)
	}
	return fmt.Sprintf("bytes=%d-%d", off, off+n-1), nil
}

func contentRangeMatchesOffset(hdr string, wantOffset int64) bool {
	hdr = strings.TrimSpace(hdr)
	if !strings.HasPrefix(strings.ToLower(hdr), "bytes ") {
		return false
	}
	rest := strings.TrimSpace(hdr[6:])
	span, _, _ := strings.Cut(rest, "/")
	startStr, _, ok := strings.Cut(span, "-")
	if !ok {
		return false
	}
	got, err := strconv.ParseInt(strings.TrimSpace(startStr), 10, 64)
	return err == nil && got == wantOffset
}

func appendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readManifest(taskDir string) (*Manifest, error) {
	p, err := SafeFilePath(taskDir, ManifestFileName)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("offline: corrupt manifest: %w", err)
	}
	return &m, nil
}

func saveManifest(taskDir string, m *Manifest) error {
	p, err := SafeFilePath(taskDir, ManifestFileName)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(p, data)
}

func clearDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
