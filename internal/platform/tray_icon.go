package platform

import "encoding/binary"

const (
	trayIconSize        = 32
	trayIconBitmapBytes = trayIconSize * trayIconSize * 4
	trayIconMaskBytes   = trayIconSize * 4
)

// saveKnotTrayIcon builds a small ICO matching the lime SaveKnot brand mark.
// Building it keeps the Windows release self-contained without an asset pipeline.
func saveKnotTrayIcon() []byte {
	const (
		fileHeaderSize   = 6
		directorySize    = 16
		bitmapHeaderSize = 40
		pixelOffset      = fileHeaderSize + directorySize + bitmapHeaderSize
		imageBytes       = bitmapHeaderSize + trayIconBitmapBytes + trayIconMaskBytes
	)
	icon := make([]byte, fileHeaderSize+directorySize+imageBytes)
	write16 := func(offset int, value uint16) { binary.LittleEndian.PutUint16(icon[offset:], value) }
	write32 := func(offset int, value uint32) { binary.LittleEndian.PutUint32(icon[offset:], value) }

	write16(2, 1) // ICO image type.
	write16(4, 1) // One image.
	icon[6], icon[7] = trayIconSize, trayIconSize
	write16(10, 1)
	write16(12, 32)
	write32(14, imageBytes)
	write32(18, fileHeaderSize+directorySize)

	write32(22, bitmapHeaderSize)
	write32(26, trayIconSize)
	write32(30, trayIconSize*2) // ICO DIB height includes the color and mask planes.
	write16(34, 1)
	write16(36, 32)
	write32(42, trayIconBitmapBytes)

	for y := 0; y < trayIconSize; y++ {
		for x := 0; x < trayIconSize; x++ {
			if !insideRoundedTrayMark(x, y) {
				continue
			}
			offset := pixelOffset + ((trayIconSize-1-y)*trayIconSize+x)*4
			icon[offset], icon[offset+1], icon[offset+2], icon[offset+3] = 0x6a, 0xf3, 0xc7, 0xff
			if insideTrayLetter(x, y) {
				icon[offset], icon[offset+1], icon[offset+2] = 0x0d, 0x20, 0x17
			}
		}
	}
	return icon
}

func insideRoundedTrayMark(x, y int) bool {
	const inset, radius = 2, 7
	if x < inset || x >= trayIconSize-inset || y < inset || y >= trayIconSize-inset {
		return false
	}
	cx := x
	if x < inset+radius {
		cx = inset + radius
	} else if x >= trayIconSize-inset-radius {
		cx = trayIconSize - inset - radius - 1
	}
	cy := y
	if y < inset+radius {
		cy = inset + radius
	} else if y >= trayIconSize-inset-radius {
		cy = trayIconSize - inset - radius - 1
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

func insideTrayLetter(x, y int) bool {
	const scale, left, top = 3, 8, 5
	letter := [...]string{
		"11111",
		"10000",
		"10000",
		"11111",
		"00001",
		"00001",
		"11111",
	}
	column, row := (x-left)/scale, (y-top)/scale
	if x < left || y < top || column < 0 || column >= len(letter[0]) || row < 0 || row >= len(letter) {
		return false
	}
	return letter[row][column] == '1'
}
