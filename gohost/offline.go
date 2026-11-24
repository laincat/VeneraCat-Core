package gohost

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/laincat/veneracat-core/video/offline"
)

// DefaultVideoCacheDir returns the default offline video cache root inside DataDir.
func (c *Core) DefaultVideoCacheDir() string {
	if c == nil || c.opts.DataDir == "" {
		return ""
	}
	return filepath.Join(c.opts.DataDir, "video-cache")
}

func (c *Core) resolveOfflineRoot(rootDir string) (string, error) {
	if strings.TrimSpace(rootDir) != "" {
		return rootDir, nil
	}
	def := c.DefaultVideoCacheDir()
	if def == "" {
		return "", errors.New("gohost: offline cache root directory is required")
	}
	return def, nil
}

// PlanVideoOffline inspects req.URL via Core's host.Transport and builds an
// offline download plan without writing media segments to disk.
func (c *Core) PlanVideoOffline(ctx context.Context, req offline.TaskRequest) (*offline.Plan, error) {
	if c == nil || c.tr == nil {
		return nil, errors.New("gohost: core transport is not initialized")
	}
	return offline.PlanTask(ctx, c.tr, req)
}

// DownloadVideoOffline executes or resumes an offline video cache task using
// Core's host.Transport. When req.RootDir is empty, DefaultVideoCacheDir is used.
func (c *Core) DownloadVideoOffline(ctx context.Context, req offline.TaskRequest, onProgress func(offline.Progress)) (*offline.Result, error) {
	if c == nil || c.tr == nil {
		return nil, errors.New("gohost: core transport is not initialized")
	}
	root, err := c.resolveOfflineRoot(req.RootDir)
	if err != nil {
		return nil, err
	}
	req.RootDir = root
	return offline.Download(ctx, c.tr, req, onProgress)
}

// InspectVideoOffline reads the persisted manifest of one cached video task.
func (c *Core) InspectVideoOffline(rootDir, taskID string) (*offline.Manifest, error) {
	root, err := c.resolveOfflineRoot(rootDir)
	if err != nil {
		return nil, err
	}
	return offline.Inspect(root, taskID)
}

// VerifyVideoOffline validates file existence, byte sizes and SHA-256 digests
// for one cached video task.
func (c *Core) VerifyVideoOffline(rootDir, taskID string) (*offline.VerifyResult, error) {
	root, err := c.resolveOfflineRoot(rootDir)
	if err != nil {
		return nil, err
	}
	return offline.Verify(root, taskID)
}

// ListVideoOffline returns all cached video tasks under rootDir (or DefaultVideoCacheDir).
func (c *Core) ListVideoOffline(rootDir string) ([]offline.Summary, error) {
	root, err := c.resolveOfflineRoot(rootDir)
	if err != nil {
		return nil, err
	}
	return offline.List(root)
}

// RemoveVideoOffline deletes a cached video task and returns reclaimed bytes.
func (c *Core) RemoveVideoOffline(rootDir, taskID string) (int64, error) {
	root, err := c.resolveOfflineRoot(rootDir)
	if err != nil {
		return 0, err
	}
	return offline.Remove(root, taskID)
}

// PruneVideoOffline removes temporary files, optional incomplete tasks, and
// evicts oldest tasks when total usage exceeds opts.MaxBytes.
func (c *Core) PruneVideoOffline(rootDir string, opts offline.PruneOptions) (*offline.PruneResult, error) {
	root, err := c.resolveOfflineRoot(rootDir)
	if err != nil {
		return nil, err
	}
	return offline.Prune(root, opts)
}
