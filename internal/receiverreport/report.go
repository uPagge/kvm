// Package receiverreport encodes existing mouse inputs in the measured USB
// receiver's report format. It does not activate a USB profile or implement
// receiver vendor commands, consumer/system inputs, or USB control behavior.
package receiverreport

import "fmt"

// EncodeMouse adapts the existing int8 movement and uint8 button inputs to
// physical Report ID 2. The upper eight button bits remain zero. Movement -128
// fits the signed 12-bit fields; wheel and pan have the narrower -127..127 range.
func EncodeMouse(dx, dy int8, buttons uint8, wheel, pan int8) ([8]byte, error) {
	if wheel == -128 || pan == -128 {
		return [8]byte{}, fmt.Errorf("receiver mouse wheel and pan must be in -127..127")
	}
	x := uint16(int16(dx)) & 0x0fff
	y := uint16(int16(dy)) & 0x0fff
	return [8]byte{
		2,
		buttons,
		0,
		byte(x),
		byte(x>>8) | byte(y<<4),
		byte(y >> 4),
		byte(wheel),
		byte(pan),
	}, nil
}
