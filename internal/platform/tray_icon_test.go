package platform

import (
	"encoding/binary"
	"testing"
)

func TestSaveKnotTrayIconIsValidICO(t *testing.T) {
	t.Parallel()
	icon := saveKnotTrayIcon()
	if len(icon) != 6+16+40+trayIconBitmapBytes+trayIconMaskBytes {
		t.Fatalf("unexpected icon size: %d", len(icon))
	}
	if binary.LittleEndian.Uint16(icon[2:]) != 1 || binary.LittleEndian.Uint16(icon[4:]) != 1 {
		t.Fatalf("invalid ICO header: %v", icon[:6])
	}
	if icon[6] != trayIconSize || icon[7] != trayIconSize || binary.LittleEndian.Uint16(icon[12:]) != 32 {
		t.Fatalf("invalid ICO image entry: %v", icon[6:22])
	}
	if binary.LittleEndian.Uint32(icon[14:]) != uint32(len(icon)-22) {
		t.Fatalf("ICO image length does not match payload")
	}
}
