package edid

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Compile the actual C boundary on Linux, replacing only syscalls. This catches
// silent checksum repair, oversized read requests and permissive C hex parsing
// without opening a capture device. It does not link the ARM native library.
func TestNativeCBoundaries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux V4L2 headers; not an ARM/native-library test")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler unavailable")
	}
	dir := t.TempDir()
	ctrl, err := os.ReadFile("../native/cgo/ctrl.c")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(ctrl), "int hex_to_bytes(")
	if start < 0 {
		t.Fatal("hex parser not found")
	}
	end := strings.Index(string(ctrl[start:]), "\n/**")
	if end < 0 {
		t.Fatal("hex parser boundary not found")
	}
	if err := os.WriteFile(filepath.Join(dir, "hex_parser.inc"), ctrl[start:start+end], 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "edid-boundaries")
	compile := exec.Command(cc, "-std=gnu11", "-Wall", "-Wextra", "-I", dir, "-I", "../native/cgo", "testdata/native_boundary.c", "-o", binary)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, output)
	}
	for _, test := range []string{"checksum", "valid_set", "disabled_set", "read_capacity", "empty_read", "oversize_read", "invalid_hex"} {
		t.Run(test, func(t *testing.T) {
			if output, err := exec.Command(binary, test).CombinedOutput(); err != nil {
				t.Fatalf("native boundary: %v\n%s", err, output)
			}
		})
	}
}
