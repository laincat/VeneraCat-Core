package adapt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PanelImageMeta identifies the storyboard panel associated with a rendered
// image handed to an ImageSink.
type PanelImageMeta struct {
	ProjectID  string
	ChapterID  string
	SceneID    string
	PanelID    string
	Characters []string
}

// PanelImageRequest is a single panel rendering request sent to an ImageGenerator.
type PanelImageRequest struct {
	ProjectID   string
	ChapterID   string
	SceneID     string
	PanelID     string
	Prompt      string
	AspectRatio string
	Size        string
	References  []ImageRef
}

// PanelImageResult is the rendered image payload and usage returned by an
// ImageGenerator.
type PanelImageResult struct {
	MIMEType      string
	Data          []byte
	RevisedPrompt string
	Usage         Usage
}

// ImageGenerator abstracts a static comic image renderer (such as Gemini or
// OpenAI Images) operating over host.Transport.
type ImageGenerator interface {
	GenerateImage(ctx context.Context, req PanelImageRequest) (*PanelImageResult, error)
}

// ImageSink persists rendered panel bytes on behalf of Core. Core never touches
// filesystem paths or permissions; callers write bytes to disk, cache, or
// memory and return an opaque reference ID recorded on Panel.ImageRefID.
type ImageSink interface {
	SaveImage(ctx context.Context, meta PanelImageMeta, mimeType string, data []byte) (refID string, err error)
}

// ImageSinkFunc adapts a function to ImageSink.
type ImageSinkFunc func(ctx context.Context, meta PanelImageMeta, mimeType string, data []byte) (string, error)

// SaveImage implements ImageSink.
func (f ImageSinkFunc) SaveImage(ctx context.Context, meta PanelImageMeta, mimeType string, data []byte) (string, error) {
	return f(ctx, meta, mimeType, data)
}

// RenderOptions configures panel and storyboard image rendering.
type RenderOptions struct {
	// AspectRatio requests a panel aspect ratio when supported by the backend
	// (for example "3:4", "4:3", "16:9", "1:1").
	AspectRatio string
	// Size requests pixel dimensions when supported by the backend (for example
	// "1024x1024", "1024x1536").
	Size string
	// MaxRetries is the number of retry attempts after an initial transient
	// failure for a single panel. ErrContentBlocked, ErrBudgetExceeded, and
	// context cancellation are never retried.
	MaxRetries int
	// ForceRerender renders panels even if their ImageStatus is already done.
	// When false, already-done panels are skipped for resumable execution.
	ForceRerender bool
	// ContinueOnError keeps rendering subsequent panels in RenderStoryboard
	// after a non-budget panel failure (such as ErrContentBlocked or exhausted
	// transient retries). ErrBudgetExceeded and context cancellation always
	// stop immediately.
	ContinueOnError bool
	// StyleReferences supplies optional local personal style reference images
	// attached to panel requests.
	StyleReferences []ImageRef
	// CharacterReferences supplies optional character turnaround / portrait
	// reference images keyed by character ID, name, or alias (case-insensitive).
	CharacterReferences map[string][]ImageRef
	// FeedbackCharacterRefs, when true, captures the first rendered image for a
	// character lacking a reference during RenderStoryboard and attaches it to
	// subsequent panels featuring that character.
	FeedbackCharacterRefs bool
	// OnStatusChange is optionally called on every ImageStatus transition.
	OnStatusChange func(sceneID, panelID string, status ImageStatus, err error)
}

// PanelFailure records one panel that failed during RenderStoryboard.
type PanelFailure struct {
	SceneID string
	PanelID string
	Err     error
}

// PanelRenderResult reports the outcome of rendering a single panel.
type PanelRenderResult struct {
	SceneID string
	PanelID string
	RefID   string
	Skipped bool
	Image   *PanelImageResult
	Usage   Usage
}

// RenderResult summarizes a RenderStoryboard run.
type RenderResult struct {
	ChapterID string
	Rendered  int
	Skipped   int
	Failed    int
	Blocked   int
	Usage     Usage
	Failures  []PanelFailure
}

// BuildPanelImagePrompt assembles a self-contained visual prompt for one panel,
// combining the panel's visual prompt, shot/action, scene setting/mood,
// matching CharacterSheet entries, and local StyleRef notes.
func BuildPanelImagePrompt(proj *Project, scene Scene, panel Panel) string {
	var b strings.Builder
	base := strings.TrimSpace(panel.Prompt)
	if base != "" {
		b.WriteString(base)
	}
	if shot := strings.TrimSpace(panel.Shot); shot != "" {
		appendSentence(&b, "Shot: "+shot)
	}
	if action := strings.TrimSpace(panel.Action); action != "" {
		appendSentence(&b, "Action: "+action)
	}
	if setting := strings.TrimSpace(scene.Setting); setting != "" {
		appendSentence(&b, "Setting: "+setting)
	}
	if mood := strings.TrimSpace(scene.Mood); mood != "" {
		appendSentence(&b, "Mood: "+mood)
	}

	if proj != nil {
		chars := resolvePanelCharacters(&proj.Characters, panel.Characters)
		if len(chars) > 0 {
			b.WriteString("\nCharacters (keep visual identity strictly consistent):")
			for _, c := range chars {
				b.WriteString("\n- ")
				b.WriteString(c.Name)
				if c.ID != "" {
					b.WriteString(" (")
					b.WriteString(c.ID)
					b.WriteByte(')')
				}
				var attrs []string
				if strings.TrimSpace(c.Appearance) != "" {
					attrs = append(attrs, "appearance="+strings.TrimSpace(c.Appearance))
				}
				if strings.TrimSpace(c.Outfit) != "" {
					attrs = append(attrs, "outfit="+strings.TrimSpace(c.Outfit))
				}
				if len(c.Traits) > 0 {
					attrs = append(attrs, "traits="+strings.Join(c.Traits, ", "))
				}
				if len(attrs) > 0 {
					b.WriteString(": ")
					b.WriteString(strings.Join(attrs, "; "))
				}
			}
		}

		if proj.StyleRef != nil {
			title := strings.TrimSpace(proj.StyleRef.ComicTitle)
			notes := strings.TrimSpace(proj.StyleRef.StyleNotes)
			if title != "" || notes != "" {
				b.WriteString("\nStyle reference (local personal style guide): ")
				if title != "" {
					b.WriteString(title)
					if notes != "" {
						b.WriteString(" - ")
					}
				}
				if notes != "" {
					b.WriteString(notes)
				}
			}
		}
	}

	b.WriteString("\nRender one static comic panel illustration without speech bubbles or text overlays.")
	return strings.TrimSpace(b.String())
}

// BuildPanelImageRequest constructs a PanelImageRequest for one panel,
// attaching style references and matching character reference images without
// mutating caller-supplied slices or maps.
func BuildPanelImageRequest(proj *Project, chapterID string, scene Scene, panel Panel, opts RenderOptions) PanelImageRequest {
	charMap := normalizeCharRefMap(opts.CharacterReferences)
	return buildPanelImageRequestWithMap(proj, chapterID, scene, panel, opts, charMap)
}

func buildPanelImageRequestWithMap(proj *Project, chapterID string, scene Scene, panel Panel, opts RenderOptions, charMap map[string][]ImageRef) PanelImageRequest {
	var projID string
	if proj != nil {
		projID = proj.ID
	}
	refs := make([]ImageRef, 0, len(opts.StyleReferences)+len(panel.Characters))
	for _, sr := range opts.StyleReferences {
		ref := cloneImageRef(sr)
		if ref.Kind == "" {
			ref.Kind = ImageRefStyle
		}
		refs = append(refs, ref)
	}

	if len(charMap) > 0 && len(panel.Characters) > 0 {
		seenKey := make(map[string]struct{})
		for _, rawName := range panel.Characters {
			keys := characterLookupKeys(proj, rawName)
			for _, k := range keys {
				list, ok := charMap[k]
				if !ok {
					continue
				}
				for idx, cr := range list {
					dedupe := fmt.Sprintf("%s#%d#%s", k, idx, cr.ID)
					if cr.ID != "" {
						dedupe = "id:" + strings.ToLower(strings.TrimSpace(cr.ID))
					}
					if _, dup := seenKey[dedupe]; dup {
						continue
					}
					seenKey[dedupe] = struct{}{}
					ref := cloneImageRef(cr)
					if ref.Kind == "" {
						ref.Kind = ImageRefCharacter
					}
					if ref.Label == "" {
						ref.Label = strings.TrimSpace(rawName)
					}
					refs = append(refs, ref)
				}
			}
		}
	}

	return PanelImageRequest{
		ProjectID:   projID,
		ChapterID:   chapterID,
		SceneID:     scene.ID,
		PanelID:     panel.ID,
		Prompt:      BuildPanelImagePrompt(proj, scene, panel),
		AspectRatio: strings.TrimSpace(opts.AspectRatio),
		Size:        strings.TrimSpace(opts.Size),
		References:  refs,
	}
}

// RenderPanel renders or retries a single panel in proj.Storyboards[chapterID].
// It advances panel.ImageStatus through pending -> generating -> done/failed,
// enforces proj.Budget via CheckNextImageCall before every attempt, never
// retries or charges budget on ErrContentBlocked, and delegates byte storage to
// sink.
func RenderPanel(ctx context.Context, proj *Project, chapterID, sceneID, panelID string, gen ImageGenerator, sink ImageSink, opts RenderOptions) (*PanelRenderResult, error) {
	if proj == nil {
		return nil, errors.New("adapt: nil project")
	}
	if gen == nil {
		return nil, errors.New("adapt: nil image generator")
	}
	sb := proj.Storyboards[chapterID]
	if sb == nil {
		return nil, fmt.Errorf("adapt: storyboard not found for chapter %q", chapterID)
	}
	si, pi, ok := findPanel(sb, sceneID, panelID)
	if !ok {
		return nil, fmt.Errorf("adapt: panel %q in scene %q not found", panelID, sceneID)
	}
	charMap := normalizeCharRefMap(opts.CharacterReferences)
	return renderPanelAt(ctx, proj, chapterID, &sb.Scenes[si], &sb.Scenes[si].Panels[pi], gen, sink, opts, charMap)
}

// RenderStoryboard renders all panels of proj.Storyboards[chapterID] in scene
// and panel order. Already-done panels are skipped unless opts.ForceRerender is
// true, enabling resumable rendering after partial runs or budget pauses.
func RenderStoryboard(ctx context.Context, proj *Project, chapterID string, gen ImageGenerator, sink ImageSink, opts RenderOptions) (*RenderResult, error) {
	if proj == nil {
		return nil, errors.New("adapt: nil project")
	}
	if gen == nil {
		return nil, errors.New("adapt: nil image generator")
	}
	sb := proj.Storyboards[chapterID]
	if sb == nil {
		return nil, fmt.Errorf("adapt: storyboard not found for chapter %q", chapterID)
	}

	out := &RenderResult{
		ChapterID: chapterID,
	}
	charMap := normalizeCharRefMap(opts.CharacterReferences)

	for si := range sb.Scenes {
		scene := &sb.Scenes[si]
		for pi := range scene.Panels {
			panel := &scene.Panels[pi]
			res, err := renderPanelAt(ctx, proj, chapterID, scene, panel, gen, sink, opts, charMap)
			if err != nil {
				if errors.Is(err, ErrBudgetExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return out, err
				}
				out.Failed++
				if errors.Is(err, ErrContentBlocked) {
					out.Blocked++
				}
				out.Failures = append(out.Failures, PanelFailure{
					SceneID: scene.ID,
					PanelID: panel.ID,
					Err:     err,
				})
				if !opts.ContinueOnError {
					return out, err
				}
				continue
			}
			if res.Skipped {
				out.Skipped++
				continue
			}
			out.Rendered++
			out.Usage.Add(res.Usage)
			if opts.FeedbackCharacterRefs && res.Image != nil && len(res.Image.Data) > 0 {
				recordFeedbackCharacterRefs(proj, panel, res, charMap)
			}
		}
	}

	return out, nil
}

func renderPanelAt(ctx context.Context, proj *Project, chapterID string, scene *Scene, panel *Panel, gen ImageGenerator, sink ImageSink, opts RenderOptions, charMap map[string][]ImageRef) (*PanelRenderResult, error) {
	if panel.ImageStatus == ImageDone && !opts.ForceRerender {
		return &PanelRenderResult{
			SceneID: scene.ID,
			PanelID: panel.ID,
			RefID:   panel.ImageRefID,
			Skipped: true,
		}, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := proj.Usage.CheckNextImageCall(proj.Budget); err != nil {
		return nil, err
	}

	req := buildPanelImageRequestWithMap(proj, chapterID, *scene, *panel, opts, charMap)
	maxRetries := opts.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	panel.ImageStatus = ImageGenerating
	panel.ImageError = ""
	notifyStatus(opts, scene.ID, panel.ID, ImageGenerating, nil)

	var imgRes *PanelImageResult
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := ctx.Err(); err != nil {
				panel.ImageStatus = ImageFailed
				panel.ImageError = err.Error()
				notifyStatus(opts, scene.ID, panel.ID, ImageFailed, err)
				return nil, err
			}
			if err := proj.Usage.CheckNextImageCall(proj.Budget); err != nil {
				panel.ImageStatus = ImageFailed
				panel.ImageError = err.Error()
				notifyStatus(opts, scene.ID, panel.ID, ImageFailed, err)
				return nil, err
			}
		}

		res, err := gen.GenerateImage(ctx, req)
		if err != nil {
			if errors.Is(err, ErrContentBlocked) || errors.Is(err, ErrBudgetExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				panel.ImageStatus = ImageFailed
				panel.ImageError = err.Error()
				notifyStatus(opts, scene.ID, panel.ID, ImageFailed, err)
				return nil, err
			}
			lastErr = err
			continue
		}
		if res == nil || len(res.Data) == 0 {
			lastErr = errors.New("adapt: image generator returned empty image data")
			continue
		}
		imgRes = res
		lastErr = nil
		break
	}

	if lastErr != nil {
		panel.ImageStatus = ImageFailed
		panel.ImageError = lastErr.Error()
		notifyStatus(opts, scene.ID, panel.ID, ImageFailed, lastErr)
		return nil, lastErr
	}

	if strings.TrimSpace(imgRes.MIMEType) == "" {
		imgRes.MIMEType = detectImageMIME(imgRes.Data)
	}

	delta := imgRes.Usage
	if delta.ImageCalls == 0 {
		delta.ImageCalls = 1
	}
	proj.Usage.Add(delta)

	if sink != nil {
		meta := PanelImageMeta{
			ProjectID:  proj.ID,
			ChapterID:  chapterID,
			SceneID:    scene.ID,
			PanelID:    panel.ID,
			Characters: append([]string(nil), panel.Characters...),
		}
		refID, err := sink.SaveImage(ctx, meta, imgRes.MIMEType, imgRes.Data)
		if err != nil {
			saveErr := fmt.Errorf("adapt: save panel image: %w", err)
			panel.ImageStatus = ImageFailed
			panel.ImageError = saveErr.Error()
			notifyStatus(opts, scene.ID, panel.ID, ImageFailed, saveErr)
			return nil, saveErr
		}
		panel.ImageRefID = refID
	}

	panel.ImageStatus = ImageDone
	panel.ImageError = ""
	notifyStatus(opts, scene.ID, panel.ID, ImageDone, nil)

	return &PanelRenderResult{
		SceneID: scene.ID,
		PanelID: panel.ID,
		RefID:   panel.ImageRefID,
		Image:   imgRes,
		Usage:   delta,
	}, nil
}

func notifyStatus(opts RenderOptions, sceneID, panelID string, status ImageStatus, err error) {
	if opts.OnStatusChange != nil {
		opts.OnStatusChange(sceneID, panelID, status, err)
	}
}

func findPanel(sb *Storyboard, sceneID, panelID string) (int, int, bool) {
	for si := range sb.Scenes {
		if sceneID != "" && sb.Scenes[si].ID != sceneID {
			continue
		}
		for pi := range sb.Scenes[si].Panels {
			if sb.Scenes[si].Panels[pi].ID == panelID {
				return si, pi, true
			}
		}
	}
	return -1, -1, false
}

func resolvePanelCharacters(sheet *CharacterSheet, names []string) []Character {
	if sheet == nil || len(sheet.Characters) == 0 || len(names) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(names))
	out := make([]Character, 0, len(names))
	for _, raw := range names {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		idx := findCharacterIndex(sheet.Characters, Character{ID: trimmed, Name: trimmed})
		if idx < 0 {
			continue
		}
		if _, dup := seen[idx]; dup {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, sheet.Characters[idx])
	}
	return out
}

func characterLookupKeys(proj *Project, raw string) []string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return nil
	}
	keys := []string{trimmed}
	if proj == nil {
		return keys
	}
	idx := findCharacterIndex(proj.Characters.Characters, Character{ID: raw, Name: raw})
	if idx < 0 {
		return keys
	}
	c := proj.Characters.Characters[idx]
	addKey := func(v string) {
		k := strings.ToLower(strings.TrimSpace(v))
		if k == "" {
			return
		}
		for _, existing := range keys {
			if existing == k {
				return
			}
		}
		keys = append(keys, k)
	}
	addKey(c.ID)
	addKey(c.Name)
	for _, a := range c.Aliases {
		addKey(a)
	}
	return keys
}

func normalizeCharRefMap(in map[string][]ImageRef) map[string][]ImageRef {
	out := make(map[string][]ImageRef, len(in))
	for k, refs := range in {
		nk := strings.ToLower(strings.TrimSpace(k))
		if nk == "" || len(refs) == 0 {
			continue
		}
		cloned := make([]ImageRef, 0, len(refs))
		for _, r := range refs {
			cloned = append(cloned, cloneImageRef(r))
		}
		out[nk] = append(out[nk], cloned...)
	}
	return out
}

func recordFeedbackCharacterRefs(proj *Project, panel *Panel, res *PanelRenderResult, charMap map[string][]ImageRef) {
	if len(panel.Characters) == 0 || res == nil || res.Image == nil || len(res.Image.Data) == 0 {
		return
	}
	for _, rawName := range panel.Characters {
		keys := characterLookupKeys(proj, rawName)
		if len(keys) == 0 {
			continue
		}
		alreadyHas := false
		for _, k := range keys {
			if len(charMap[k]) > 0 {
				alreadyHas = true
				break
			}
		}
		if alreadyHas {
			continue
		}
		label := strings.TrimSpace(rawName)
		if proj != nil {
			if idx := findCharacterIndex(proj.Characters.Characters, Character{ID: rawName, Name: rawName}); idx >= 0 {
				label = proj.Characters.Characters[idx].Name
			}
		}
		ref := ImageRef{
			ID:       res.RefID,
			Kind:     ImageRefCharacter,
			Label:    label,
			MIMEType: res.Image.MIMEType,
			Data:     bytes.Clone(res.Image.Data),
		}
		if ref.ID == "" {
			ref.ID = "feedback:" + panel.ID
		}
		for _, k := range keys {
			charMap[k] = []ImageRef{cloneImageRef(ref)}
		}
	}
}

func cloneImageRef(ref ImageRef) ImageRef {
	out := ref
	if len(ref.Data) > 0 {
		out.Data = bytes.Clone(ref.Data)
	}
	return out
}

func appendSentence(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	if b.Len() > 0 {
		if !strings.HasSuffix(b.String(), ".") && !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('.')
		}
		b.WriteByte(' ')
	}
	b.WriteString(s)
}

func validateAPIBaseURL(raw, defaultURL, label string) (string, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		base = defaultURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("adapt: %s base URL is not valid", label)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("adapt: %s base URL must use http or https", label)
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("adapt: %s base URL has no host", label)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("adapt: %s base URL must not contain credentials", label)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("adapt: %s base URL must not contain query or fragment", label)
	}
	return strings.TrimRight(base, "/"), nil
}

func sanitizeSecret(text, secret string) string {
	if secret == "" || text == "" {
		return text
	}
	clean := strings.ReplaceAll(text, secret, "[REDACTED]")
	if escaped := url.QueryEscape(secret); escaped != "" && escaped != secret {
		clean = strings.ReplaceAll(clean, escaped, "[REDACTED]")
	}
	if trimmed := strings.TrimSpace(secret); trimmed != "" && trimmed != secret {
		clean = strings.ReplaceAll(clean, trimmed, "[REDACTED]")
		if escaped := url.QueryEscape(trimmed); escaped != "" && escaped != trimmed {
			clean = strings.ReplaceAll(clean, escaped, "[REDACTED]")
		}
	}
	return clean
}

func redactError(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	msg := err.Error()
	clean := sanitizeSecret(msg, secret)
	if clean == msg {
		return err
	}
	for _, sentinel := range []error{ErrContentBlocked, ErrBudgetExceeded, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			detail := strings.TrimPrefix(clean, sentinel.Error()+": ")
			if detail == clean {
				return fmt.Errorf("%w: %s", sentinel, clean)
			}
			return fmt.Errorf("%w: %s", sentinel, detail)
		}
	}
	return errors.New(clean)
}

func detectImageMIME(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	detected := http.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") {
		return detected
	}
	return "application/octet-stream"
}

func extForMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}
