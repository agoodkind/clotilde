package mitm

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// decodeForCapture transparently decompresses captured response
// bytes when a Content-Encoding the standard library can handle is
// present. Forward bytes to the client are unaffected. Returns the
// original bytes when the encoding is unknown (zstd, brotli) or when
// decompression fails. The boolean return reports whether
// decompression actually ran.
func decodeForCapture(raw []byte, contentEncoding string) ([]byte, bool) {
	enc := strings.TrimSpace(strings.ToLower(contentEncoding))
	if enc == "" || enc == "identity" {
		return raw, false
	}
	switch enc {
	case "gzip", "x-gzip":
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return raw, false
		}
		defer gr.Close()
		out, err := io.ReadAll(gr)
		if err != nil {
			return raw, false
		}
		return out, true
	case "deflate":
		// Some servers send raw RFC 1951 deflate; others send RFC 1950 zlib.
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err == nil {
			defer zr.Close()
			out, rerr := io.ReadAll(zr)
			if rerr == nil {
				return out, true
			}
		}
		fr := flate.NewReader(bytes.NewReader(raw))
		defer fr.Close()
		out, err := io.ReadAll(fr)
		if err != nil {
			return raw, false
		}
		return out, true
	case "zstd", "zstandard":
		dec, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			return raw, false
		}
		defer dec.Close()
		out, err := io.ReadAll(dec)
		if err != nil {
			return raw, false
		}
		return out, true
	}
	return raw, false
}
