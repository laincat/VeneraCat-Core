package mobile

import (
	"encoding/json"
	"fmt"

	"github.com/laincat/veneracat-core/video/offline"
)

// VideoOfflinePlan inspects an HLS or direct media URL through Host.HTTP and
// returns a JSON-encoded offline.Plan without writing media segments to disk.
// Callers pass a callID from NewCall() so Cancel(callID) aborts in-flight HTTP.
func (c *Core) VideoOfflinePlan(callID int64, requestJSON string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	var req offline.TaskRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("mobile: invalid offline plan request JSON: %w", err)
	}
	ctx, done := c.ctx(callID)
	defer done()
	plan, err := offline.PlanTask(ctx, c.bridge, req)
	if err != nil {
		return "", err
	}
	return encode(plan)
}

// VideoOfflineDownload executes or resumes an offline video cache task through
// Host.HTTP, writing verified segments/media and a local rewritten index.m3u8
// under req.RootDir/<req.TaskID>. Cancel(callID) aborts active HTTP calls and
// releases handles and temporary files cleanly.
func (c *Core) VideoOfflineDownload(callID int64, requestJSON string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	var req offline.TaskRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("mobile: invalid offline download request JSON: %w", err)
	}
	ctx, done := c.ctx(callID)
	defer done()
	res, err := offline.Download(ctx, c.bridge, req, nil)
	if err != nil {
		return "", err
	}
	return encode(res)
}

// VideoOfflineInspect returns the persisted offline.Manifest JSON of taskID.
func (c *Core) VideoOfflineInspect(rootDir, taskID string) (_ string, err error) {
	defer guard(&err)
	m, err := offline.Inspect(rootDir, taskID)
	if err != nil {
		return "", err
	}
	return encode(m)
}

// VideoOfflineVerify verifies all recorded files and SHA-256 checksums of
// taskID under rootDir and returns an offline.VerifyResult JSON document.
func (c *Core) VideoOfflineVerify(rootDir, taskID string) (_ string, err error) {
	defer guard(&err)
	res, err := offline.Verify(rootDir, taskID)
	if err != nil {
		return "", err
	}
	return encode(res)
}

// VideoOfflineList lists cached video tasks under rootDir as JSON.
func (c *Core) VideoOfflineList(rootDir string) (_ string, err error) {
	defer guard(&err)
	list, err := offline.List(rootDir)
	if err != nil {
		return "", err
	}
	return encode(list)
}

// VideoOfflineRemove removes one cached video task directory safely and returns
// {"taskId":"...","reclaimedBytes":n}.
func (c *Core) VideoOfflineRemove(rootDir, taskID string) (_ string, err error) {
	defer guard(&err)
	freed, err := offline.Remove(rootDir, taskID)
	if err != nil {
		return "", err
	}
	return encode(struct {
		TaskID         string `json:"taskId"`
		ReclaimedBytes int64  `json:"reclaimedBytes"`
	}{
		TaskID:         taskID,
		ReclaimedBytes: freed,
	})
}

// VideoOfflinePrune cleans orphaned temporary files, optional incomplete tasks,
// and evicts oldest tasks above maxBytes. optionsJSON mirrors offline.PruneOptions.
func (c *Core) VideoOfflinePrune(rootDir, optionsJSON string) (_ string, err error) {
	defer guard(&err)
	var opts offline.PruneOptions
	if optionsJSON != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
			return "", fmt.Errorf("mobile: invalid offline prune options JSON: %w", err)
		}
	}
	res, err := offline.Prune(rootDir, opts)
	if err != nil {
		return "", err
	}
	return encode(res)
}
