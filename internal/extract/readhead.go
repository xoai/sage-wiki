package extract

import (
	"os"
	"unicode/utf8"
)

// ReadHead reads the first maxRunes runes from a file.
// Returns empty string on error or if file doesn't exist.
func ReadHead(path string, maxRunes int) string {
	// Read more bytes than needed to handle multi-byte runes
	maxBytes := maxRunes * 4
	if maxBytes > 8192 {
		maxBytes = 8192
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, maxBytes)
	n, _ := f.Read(buf)
	buf = buf[:n]

	// Count runes and truncate
	runes := 0
	i := 0
	for i < len(buf) && runes < maxRunes {
		_, size := utf8.DecodeRune(buf[i:])
		i += size
		runes++
	}

	return string(buf[:i])
}
