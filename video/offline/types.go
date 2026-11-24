package offline

import "net/http"

// Kind identifies whether a cached video is an HLS playlist or a direct media file.
type Kind string

const (
	KindAuto   Kind = "auto"
	KindHLS    Kind = "hls"
	KindDirect Kind = "direct"
)

// Status describes the lifecycle state of an offline cache task.
type Status string

const (
	StatusPlanned     Status = "planned"
	StatusDownloading Status = "downloading"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
)

// ItemKind identifies one asset stored inside a task directory.
type ItemKind string

const (
	ItemSegment ItemKind = "segment"
	ItemKey     ItemKind = "key"
	ItemMap     ItemKind = "map"
	ItemMedia   ItemKind = "media"
)

const (
	// ManifestVersion is written to manifest.json.
	ManifestVersion = 1
	// ManifestFileName is the metadata file stored inside each task directory.
	ManifestFileName = "manifest.json"
	// DefaultPlaylistName is the local rewritten playlist written for HLS tasks.
	DefaultPlaylistName = "index.m3u8"
	// DefaultMaxPlaylistBytes bounds playlist downloads during planning.
	DefaultMaxPlaylistBytes int64 = 16 << 20
	// DefaultMaxSegmentBytes caps a single HLS segment/key/map download.
	DefaultMaxSegmentBytes int64 = 64 << 20
	// DefaultMaxSegments caps segments in one offline HLS task.
	DefaultMaxSegments = 20000
)

// TaskRequest configures planning or executing an offline video cache task.
type TaskRequest struct {
	RootDir         string      `json:"rootDir"`
	TaskID          string      `json:"taskId"`
	URL             string      `json:"url"`
	Kind            Kind        `json:"kind,omitempty"`
	Headers         http.Header `json:"headers,omitempty"`
	Cookies         bool        `json:"cookies,omitempty"`
	Partition       string      `json:"partition,omitempty"`
	DropAds         bool        `json:"dropAds,omitempty"`
	StripPrefix     bool        `json:"stripPrefix,omitempty"`
	MaxHeight       int         `json:"maxHeight,omitempty"`
	MaxSegmentBytes int64       `json:"maxSegmentBytes,omitempty"`
	MaxTotalBytes   int64       `json:"maxTotalBytes,omitempty"`
	MaxSegments     int         `json:"maxSegments,omitempty"`
	ExpectedSHA256  string      `json:"expectedSha256,omitempty"`
	ExpectedBytes   int64       `json:"expectedBytes,omitempty"`
	Concurrency     int         `json:"concurrency,omitempty"`
}

// PlannedItem describes one remote asset to fetch and its relative local path.
type PlannedItem struct {
	Kind      ItemKind `json:"kind"`
	Index     int      `json:"index"`
	Sequence  int64    `json:"sequence,omitempty"`
	URL       string   `json:"url"`
	LocalPath string   `json:"localPath"`
	ByteRange string   `json:"byteRange,omitempty"`
	Duration  float64  `json:"duration,omitempty"`
}

// Plan is the download plan produced before writing media segments to disk.
type Plan struct {
	TaskID        string        `json:"taskId"`
	Kind          Kind          `json:"kind"`
	SourceURL     string        `json:"sourceUrl"`
	MediaURL      string        `json:"mediaUrl"`
	EntryPath     string        `json:"entryPath"`
	Items         []PlannedItem `json:"items"`
	TotalDuration float64       `json:"totalDuration,omitempty"`
	DroppedAds    int           `json:"droppedAds,omitempty"`
	Warnings      []string      `json:"warnings,omitempty"`
	RewrittenM3U8 string        `json:"rewrittenM3U8,omitempty"`
}

// ManifestItem persists the planned asset and its verified on-disk state.
type ManifestItem struct {
	Kind      ItemKind `json:"kind"`
	Index     int      `json:"index"`
	Sequence  int64    `json:"sequence,omitempty"`
	URL       string   `json:"url"`
	LocalPath string   `json:"localPath"`
	ByteRange string   `json:"byteRange,omitempty"`
	Duration  float64  `json:"duration,omitempty"`
	Completed bool     `json:"completed"`
	Bytes     int64    `json:"bytes,omitempty"`
	SHA256    string   `json:"sha256,omitempty"`
	UpdatedAt int64    `json:"updatedAt,omitempty"`
}

// Manifest is persisted to <taskDir>/manifest.json to enable resume,
// integrity checks, and cache inspection.
type Manifest struct {
	Version       int            `json:"version"`
	TaskID        string         `json:"taskId"`
	Kind          Kind           `json:"kind"`
	Status        Status         `json:"status"`
	SourceURL     string         `json:"sourceUrl"`
	MediaURL      string         `json:"mediaUrl"`
	EntryPath     string         `json:"entryPath"`
	EntrySHA256   string         `json:"entrySha256,omitempty"`
	EntryBytes    int64          `json:"entryBytes,omitempty"`
	RewrittenM3U8 string         `json:"rewrittenM3U8,omitempty"`
	TotalDuration float64        `json:"totalDuration,omitempty"`
	DroppedAds    int            `json:"droppedAds,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
	Items         []ManifestItem `json:"items"`
	CreatedAt     int64          `json:"createdAt"`
	UpdatedAt     int64          `json:"updatedAt"`
	LastError     string         `json:"lastError,omitempty"`
}

// Progress reports incremental download state.
type Progress struct {
	TaskID          string `json:"taskId"`
	Status          Status `json:"status"`
	CompletedItems  int    `json:"completedItems"`
	TotalItems      int    `json:"totalItems"`
	DownloadedBytes int64  `json:"downloadedBytes"`
	TotalBytes      int64  `json:"totalBytes"`
	CurrentItem     string `json:"currentItem,omitempty"`
}

// Result summarizes one Download call.
type Result struct {
	TaskID          string   `json:"taskId"`
	Kind            Kind     `json:"kind"`
	Status          Status   `json:"status"`
	TaskDir         string   `json:"taskDir"`
	EntryPath       string   `json:"entryPath"`
	EntryFile       string   `json:"entryFile"`
	CompletedItems  int      `json:"completedItems"`
	TotalItems      int      `json:"totalItems"`
	SkippedItems    int      `json:"skippedItems"`
	DownloadedBytes int64    `json:"downloadedBytes"`
	TotalBytes      int64    `json:"totalBytes"`
	Warnings        []string `json:"warnings,omitempty"`
}

// VerifyResult reports the integrity state of a cached task on disk.
type VerifyResult struct {
	TaskID       string   `json:"taskId"`
	Valid        bool     `json:"valid"`
	Status       Status   `json:"status"`
	EntryFile    string   `json:"entryFile,omitempty"`
	CheckedItems int      `json:"checkedItems"`
	TotalBytes   int64    `json:"totalBytes"`
	Missing      []string `json:"missing,omitempty"`
	Corrupted    []string `json:"corrupted,omitempty"`
}

// Summary describes one task discovered under a cache root directory.
type Summary struct {
	TaskID         string `json:"taskId"`
	Kind           Kind   `json:"kind"`
	Status         Status `json:"status"`
	SourceURL      string `json:"sourceUrl"`
	EntryPath      string `json:"entryPath"`
	EntryFile      string `json:"entryFile"`
	CompletedItems int    `json:"completedItems"`
	TotalItems     int    `json:"totalItems"`
	DiskBytes      int64  `json:"diskBytes"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

// PruneOptions configures cache cleanup under a root directory.
type PruneOptions struct {
	// RemoveIncomplete deletes tasks whose status is not StatusCompleted.
	RemoveIncomplete bool `json:"removeIncomplete,omitempty"`
	// MaxBytes evicts the oldest tasks (by UpdatedAt) until total disk usage
	// is at or below MaxBytes. Zero disables size eviction.
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// PruneResult summarizes a Prune operation.
type PruneResult struct {
	RemovedTasks   []string `json:"removedTasks,omitempty"`
	ReclaimedBytes int64    `json:"reclaimedBytes"`
	RemainingBytes int64    `json:"remainingBytes"`
}
