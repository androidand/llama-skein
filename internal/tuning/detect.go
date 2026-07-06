package tuning

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// deviceGfx maps AMD PCI device IDs to their gfx target. Detection is
// authoritative (a profile's device_ids list is only documentation). Extend
// this as new cards are added.
var deviceGfx = map[uint32]string{
	// Navi 48 (RDNA4) — R9700, RX 9070 / 9070 XT
	0x7550: "gfx1201", 0x7551: "gfx1201",
	// Navi 31 (RDNA3) — RX 7900 XTX/XT, W7800/W7900
	0x744c: "gfx1100", 0x7448: "gfx1100", 0x7449: "gfx1100",
	// Navi 32 (RDNA3) — RX 7800 XT / 7700 XT
	0x747e: "gfx1101",
	// Navi 21 (RDNA2) — RX 6800 / 6800 XT / 6900 XT / 6950 XT
	0x73bf: "gfx1030", 0x73a5: "gfx1030", 0x73af: "gfx1030",
}

const amdVendorID = 0x1002

// DetectGfx scans sysfsRoot (normally "/sys") for an AMD GPU and returns its
// gfx target and PCI device ID. It reads only device/vendor IDs, so it needs
// no ROCm tooling. ok is false when no known AMD GPU is found.
func DetectGfx(sysfsRoot string) (gfx string, deviceID uint32, ok bool) {
	cards, _ := filepath.Glob(filepath.Join(sysfsRoot, "class", "drm", "card*", "device"))
	sort.Strings(cards) // deterministic when several cards are present
	for _, dev := range cards {
		vendor := readHexID(filepath.Join(dev, "vendor"))
		if vendor != amdVendorID {
			continue
		}
		id := readHexID(filepath.Join(dev, "device"))
		if g, found := deviceGfx[id]; found {
			return g, id, true
		}
	}
	return "", 0, false
}

// readHexID reads a sysfs id file like "0x1002\n" and returns its value, or 0.
func readHexID(path string) uint32 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(raw))
	s = strings.TrimPrefix(s, "0x")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}
