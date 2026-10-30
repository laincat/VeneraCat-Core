package js

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/laincat/veneracat-core/comic/image"
)

// ErrModifyImage reports a modifyImage script that did not produce an image.
var ErrModifyImage = errors.New("js: modifyImage did not return an image")

// ModifyImage runs a source's modifyImage script over one decoded bitmap.
//
// A source may return a modifyImage script in place of plain bytes: it receives
// an Image and returns another one, which is how jm reorders the blocks of a
// long strip and how several sources rotate a page. The script runs in its own
// fresh runtime — it declares its own globals, so sharing the source's engine
// would let two images overwrite each other's names — and the bitmaps live in
// that runtime's store. Decoding the fetched bytes and encoding the result stay
// the client's job; this function only moves RGBA.
func (r *Runtime) ModifyImage(ctx context.Context, script string, src image.Bitmap) (image.Bitmap, error) {
	if src.W <= 0 || src.H <= 0 || len(src.Pix) != src.W*src.H*4 {
		return image.Bitmap{}, fmt.Errorf("js: modifyImage input: %w", image.ErrInvalid)
	}
	if script == "" {
		return image.Bitmap{}, ErrModifyImage
	}
	e, err := r.newEngine("", "modify", newScratchStorage())
	if err != nil {
		return image.Bitmap{}, err
	}
	defer e.close()
	key, err := e.image.Put(&image.Bitmap{W: src.W, H: src.H, Pix: append([]byte(nil), src.Pix...)})
	if err != nil {
		return image.Bitmap{}, err
	}
	// The script is the source's own text: it defines modifyImage and may use
	// any global the prelude publishes, but it has no source storage or
	// network of its own.
	body := "(function(){" + script + "\n;if(typeof modifyImage!=='function')throw new Error('" + errNoFn + "');" +
		"return Promise.resolve(modifyImage(new Image(" + strconv.Itoa(key) + "))).then(function(v){" +
		"if(!v||typeof v.key!=='number')throw new Error('" + errNoModify + "');return v.key})})()"
	raw, err := evalOn(ctx, e, body)
	if err != nil {
		if isNoFn(err) {
			return image.Bitmap{}, ErrModifyImage
		}
		return image.Bitmap{}, err
	}
	var out int
	if err := json.Unmarshal(raw, &out); err != nil {
		return image.Bitmap{}, ErrModifyImage
	}
	got, err := e.image.Get(out)
	if err != nil {
		return image.Bitmap{}, err
	}
	return image.Bitmap{W: got.W, H: got.H, Pix: append([]byte(nil), got.Pix...)}, nil
}

const errNoModify = "__vc_no_image__"

func isNoFn(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, errNoFn) || strings.Contains(msg, errNoModify)
}
