// Package edid validates EDID data before it reaches the capture device.
package edid

import (
	"bytes"
	"encoding/hex"
	"fmt"
)

type timing struct{ vertical, horizontal, clock float64 }
type limits struct{ minV, maxV, minH, maxH, maxClock float64 }

// ValidateSemantics additionally checks timing/range coherence for profiles we
// author. External monitor range interpretation is deliberately outside the
// general structural validator; authored profiles use the stricter contract.
func ValidateSemantics(value string) error { return validate(value, true) }

// ValidateHex checks the capture chip's supported 128/256-byte EDID transport
// and structure. It does not impose the candidate's timing/range contract.
// It accepts custom resolutions/audio and unknown extension types; it does not
// attest that a mode is supported by JetKVM hardware. Empty/default and disabled
// control values belong to the caller and are deliberately rejected here.
func ValidateHex(value string) error {
	return validate(value, false)
}

func validate(value string, semantic bool) error {
	if len(value) != 256 && len(value) != 512 {
		return fmt.Errorf("EDID must contain 128 or 256 bytes")
	}
	b, err := hex.DecodeString(value)
	if err != nil {
		return fmt.Errorf("invalid EDID hex: %w", err)
	}
	if !bytes.Equal(b[:8], []byte{0, 255, 255, 255, 255, 255, 255, 0}) {
		return fmt.Errorf("invalid EDID header")
	}
	if int(b[126]) != len(b)/128-1 {
		return fmt.Errorf("EDID extension count does not match length")
	}
	for offset := 0; offset < len(b); offset += 128 {
		var sum byte
		for _, v := range b[offset : offset+128] {
			sum += v
		}
		if sum != 0 {
			return fmt.Errorf("invalid EDID checksum in block %d", offset/128)
		}
	}
	var modes []timing
	var ranges []limits
	for i := 54; i < 126; i += 18 {
		d := b[i : i+18]
		if d[0] != 0 || d[1] != 0 {
			mode, err := parseTiming(d)
			if err != nil {
				return fmt.Errorf("EDID descriptor %d: %w", (i-54)/18, err)
			}
			modes = append(modes, mode)
			continue
		}
		if d[2] != 0 {
			return fmt.Errorf("invalid monitor descriptor header")
		}
		if d[3] == 0xfd && semantic {
			// EDID 1.4 uses byte 4 for range offsets. Preserve these custom
			// profiles; this checker only interprets the unextended ranges.
			if d[4] != 0 {
				continue
			}
			r := limits{float64(d[5]), float64(d[6]), float64(d[7]), float64(d[8]), float64(d[9]) * 10}
			if r.minV == 0 || r.minH == 0 || r.minV > r.maxV || r.minH > r.maxH {
				return fmt.Errorf("invalid monitor range limits")
			}
			ranges = append(ranges, r)
		}
	}
	for i := 38; i < 54; i += 2 {
		if b[i] == 1 && b[i+1] == 1 {
			continue
		}
		if b[i] < 1 {
			return fmt.Errorf("invalid standard timing at byte %d", i)
		}
		modes = append(modes, timing{vertical: float64(b[i+1]&63) + 60})
	}
	if len(b) == 256 && b[128] == 2 {
		ctaModes, err := parseCTA(b[128:], semantic)
		if err != nil {
			return err
		}
		modes = append(modes, ctaModes...)
	}
	for _, r := range ranges {
		for _, mode := range modes {
			// Range fields are rounded to integer Hz/kHz, pixel limits to 10 MHz.
			if mode.vertical < r.minV-1 || mode.vertical > r.maxV+1 {
				return fmt.Errorf("timing %.3f Hz contradicts monitor range %.0f..%.0f Hz", mode.vertical, r.minV, r.maxV)
			}
			if mode.horizontal > 0 && (mode.horizontal < r.minH-1 || mode.horizontal > r.maxH+1) {
				return fmt.Errorf("timing %.3f kHz contradicts monitor range %.0f..%.0f kHz", mode.horizontal, r.minH, r.maxH)
			}
			if r.maxClock > 0 && mode.clock > r.maxClock {
				return fmt.Errorf("timing %.3f MHz exceeds monitor pixel clock %.0f MHz", mode.clock, r.maxClock)
			}
		}
	}
	return nil
}

func parseTiming(d []byte) (timing, error) {
	clock := float64(int(d[0])|int(d[1])<<8) * 10000
	hActive := int(d[2]) | int(d[4]&0xf0)<<4
	hBlank := int(d[3]) | int(d[4]&15)<<8
	vActive := int(d[5]) | int(d[7]&0xf0)<<4
	vBlank := int(d[6]) | int(d[7]&15)<<8
	hOffset := int(d[8]) | int(d[11]&0xc0)<<2
	hWidth := int(d[9]) | int(d[11]&0x30)<<4
	vOffset := int(d[10]>>4) | int(d[11]&0x0c)<<2
	vWidth := int(d[10]&15) | int(d[11]&3)<<4
	if clock == 0 || hActive == 0 || vActive == 0 || hBlank == 0 || vBlank == 0 || hWidth == 0 || vWidth == 0 || hOffset+hWidth > hBlank || vOffset+vWidth > vBlank {
		return timing{}, fmt.Errorf("invalid detailed timing dimensions/blanking/sync")
	}
	hTotal, vTotal := float64(hActive+hBlank), float64(vActive+vBlank)
	if d[17]&0x80 != 0 {
		vTotal += 0.5
	} // Interlaced DTD encodes one field.
	return timing{vertical: clock / hTotal / vTotal, horizontal: clock / hTotal / 1000, clock: clock / 1000000}, nil
}

func parseCTA(c []byte, semantic bool) ([]timing, error) {
	end := int(c[2])
	if end == 0 {
		if !allZero(c[4:127]) {
			return nil, fmt.Errorf("CTA without data/DTD offset contains data")
		}
		return nil, nil
	}
	if end < 4 || end > 127 {
		return nil, fmt.Errorf("invalid CTA data/DTD offset")
	}
	var modes []timing
	for i := 4; i < end; {
		tag, size := c[i]>>5, int(c[i]&31)
		if i+1+size > end {
			return nil, fmt.Errorf("CTA data block exceeds collection bounds")
		}
		payload := c[i+1 : i+1+size]
		switch tag {
		case 1:
			if size == 0 || size%3 != 0 {
				return nil, fmt.Errorf("invalid CTA audio descriptor length")
			}
		case 2:
			for _, code := range payload {
				if code == 0 || code == 128 {
					return nil, fmt.Errorf("invalid CTA video code")
				}
				// 129..192 encode a native VIC; 193..253 are extended VICs.
				if code >= 129 && code <= 192 {
					code &= 127
				}
				if mode, ok := ctaTimings[code]; ok {
					modes = append(modes, mode)
				}
			}
		case 3:
			if size < 3 {
				return nil, fmt.Errorf("truncated CTA vendor identifier")
			}
		case 4:
			if size != 3 {
				return nil, fmt.Errorf("invalid CTA speaker descriptor length")
			}
		case 7:
			if size == 0 {
				return nil, fmt.Errorf("missing CTA extended tag")
			}
		}
		i += size + 1
	}
	dtdCount := 0
	for i := end; i < 127; {
		if allZero(c[i:127]) {
			break
		}
		if i+18 > 127 {
			return nil, fmt.Errorf("CTA detailed timing exceeds extension bounds")
		}
		mode, err := parseTiming(c[i : i+18])
		if err != nil {
			return nil, fmt.Errorf("CTA: %w", err)
		}
		modes = append(modes, mode)
		dtdCount++
		i += 18
	}
	if semantic && int(c[3]&15) > dtdCount {
		return nil, fmt.Errorf("CTA native DTD count exceeds detailed timings")
	}
	return modes, nil
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// Common CTA timings needed to detect contradictory fallback advertisements.
// Unknown VICs remain valid custom modes; they are not hardware promises.
var ctaTimings = map[byte]timing{
	1: {59.94, 31.469, 25.175}, 2: {59.94, 31.469, 27}, 3: {59.94, 31.469, 27},
	4: {60, 45, 74.25}, 5: {60, 33.75, 74.25}, 16: {60, 67.5, 148.5},
	17: {50, 31.25, 27}, 18: {50, 31.25, 27}, 19: {50, 37.5, 74.25}, 20: {50, 28.125, 74.25},
	31: {50, 56.25, 148.5}, 32: {24, 27, 74.25}, 33: {25, 28.125, 74.25}, 34: {30, 33.75, 74.25},
	60: {24, 18, 59.4}, 61: {25, 18.75, 74.25}, 62: {30, 22.5, 74.25},
	63: {120, 135, 297}, 64: {100, 112.5, 297},
}
