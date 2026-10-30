package convert

import (
	"bytes"
	"testing"
)

// A server put a BOM in front of a base64 body, the decode failed at byte zero,
// and the source then had nothing to decrypt: it reported a null dereference
// instead of the real problem and never reached its own fallback list.
func TestBase64ToleratesTransportArtefacts(t *testing.T) {
	want := []byte("hello")
	plain := "aGVsbG8="
	cases := map[string]string{
		"bom":           "\xef\xbb\xbf" + plain,
		"trailing crlf": plain + "\r\n",
		"wrapped":       "aGVs\r\nbG8=",
		"spaces":        " aGVs bG8= ",
	}
	for name, in := range cases {
		out, err := Do(Message{Type: "base64", Value: in})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(out.([]byte), want) {
			t.Errorf("%s: got %q", name, out)
		}
	}
	// Input that is not base64 at all still fails: this is not a licence to
	// decode anything.
	if _, err := Do(Message{Type: "base64", Value: "\xef\xbb\xbfnot base64!!"}); err == nil {
		t.Error("garbage decoded without an error")
	}
}
