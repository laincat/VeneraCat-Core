package hls

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	tsSyncRun    = 5
)

// StripSegmentPrefix removes junk (for example a fake PNG or GIF header)
// that some sites prepend to MPEG-TS segments. It looks for the first offset
// p where b[p+188*k] == 0x47 for at least five consecutive packets and
// returns b[p:], which shares memory with the input. The input is returned
// unchanged when p is 0 or when no such offset exists.
func StripSegmentPrefix(b []byte) []byte {
	last := len(b) - tsPacketSize*(tsSyncRun-1) // exclusive bound for p
	for p := 0; p < last; p++ {
		if b[p] != tsSyncByte {
			continue
		}
		ok := true
		for k := 1; k < tsSyncRun; k++ {
			if b[p+tsPacketSize*k] != tsSyncByte {
				ok = false
				break
			}
		}
		if ok {
			return b[p:]
		}
	}
	return b
}
