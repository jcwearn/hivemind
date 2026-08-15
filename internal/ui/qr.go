package ui

import (
	"strconv"
	"strings"

	"rsc.io/qr"
)

// quietZone is the mandatory clear margin around a QR code, in modules. Four is
// the spec minimum; anything less and phone scanners start refusing codes that
// sit against a coloured background.
const quietZone = 4

// QR holds a rendered code, ready to drop into an <svg>.
type QR struct {
	// Path is SVG path data covering every dark module.
	Path string
	// Extent is the viewBox width and height, in modules.
	Extent int
}

// NewQR encodes text as a QR code and flattens it into a single SVG path.
//
// One path rather than one <rect> per module: a code for a URL of this length
// runs to roughly a thousand dark modules, and a thousand elements is a
// measurably slower parse and a much larger document than the same shape
// expressed as path commands. It also scales cleanly, which matters because the
// join code is going on whatever television the host owns.
//
// Level M is the middle error correction level. It survives a phone camera at
// an angle across a room, without inflating the code the way Q or H would.
func NewQR(text string) (QR, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return QR{}, err
	}

	var b strings.Builder
	for y := range code.Size {
		for x := range code.Size {
			if !code.Black(x, y) {
				continue
			}
			// One unit square per dark module, drawn with relative commands so
			// the digits stay short.
			b.WriteByte('M')
			b.WriteString(strconv.Itoa(x + quietZone))
			b.WriteByte(' ')
			b.WriteString(strconv.Itoa(y + quietZone))
			b.WriteString("h1v1h-1z")
		}
	}

	return QR{Path: b.String(), Extent: code.Size + 2*quietZone}, nil
}
