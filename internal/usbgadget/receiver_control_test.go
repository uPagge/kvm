package usbgadget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jetkvm/kvm/internal/receivercontrol"
	"golang.org/x/sys/unix"
)

func syntheticControlReference(t *testing.T) *receivercontrol.Reference {
	t.Helper()
	b, err := os.ReadFile("testdata/receiver-control-synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := receivercontrol.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestReceiverControlPendingBeforeMutation(t *testing.T) {
	u := standardTestGadget(t)
	u.receiverControl.reference = &receivercontrol.Reference{}
	before := u.GetGadgetDevices()
	if err := u.SetGadgetDevices(&Devices{Profile: HIDProfileReceiverEnumeration}); err == nil {
		t.Fatal("pending reference accepted")
	}
	if u.GetGadgetDevices() != before || u.configMap["relative_mouse"].attrs["report_contract"] != "2" {
		t.Fatal("readiness failure mutated gadget")
	}
}
func TestReceiverControlWriterNoActivityAndGeneration(t *testing.T) {
	u := receiverTestGadget(t)
	r, w := reportPipe(t)
	u.relMouseHidFile = w
	before := u.GetLastUserInputTime()
	u.relMouseButtons = 0x81
	generation := u.receiverControl.generation
	reply := []byte{0x10, 0xff, 0x81, 0, 0, 9, 0}
	if err := u.writeReceiverReply(context.Background(), generation, reply); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, reply)
	if u.relMouseButtons != 0x81 || !u.GetLastUserInputTime().Equal(before) {
		t.Fatal("vendor reply changed user state")
	}
	u.stopReceiverControl()
	if err := u.writeReceiverReply(context.Background(), generation, reply); !errors.Is(err, context.Canceled) {
		t.Fatalf("old generation write: %v", err)
	}
	if err := u.writeReceiverReply(context.Background(), u.receiverControl.generation, []byte{2, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("vendor worker emitted pointer report")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// SetWriteDeadline on a closed pipe returns a platform-specific poll error.
	// The writer must preserve that error and retire the cached descriptor.
	closedErr := w.SetWriteDeadline(time.Now())
	if closedErr == nil {
		t.Fatal("closed pipe accepted a write deadline")
	}
	if err := u.writeReceiverReply(context.Background(), u.receiverControl.generation, reply); !errors.Is(err, closedErr) {
		t.Fatalf("closed writer result = %v, want %v", err, closedErr)
	}
	if u.relMouseHidFile != nil {
		t.Fatal("failed writer FD was not reset")
	}
}
func TestReceiverControlOwnedWorkerCloseAndReadFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "malformed"}[fail], func(t *testing.T) {
			u := receiverTestGadget(t)
			u.udc = "synthetic.usb"
			u.receiverControl.owned = true
			fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.SetNonblock(fds[0], true); err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fds[1])
			r, w := reportPipe(t)
			u.relMouseHidFile = w
			u.receiverControl.openReader = func(path string, flags int) (int, error) {
				if path != "/dev/hidg7" || flags != unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC {
					t.Errorf("reader path/flags %s/%x", path, flags)
				}
				return fds[0], nil
			}
			if err := u.startReceiverControl(); err != nil {
				t.Fatal(err)
			}
			if _, err := unix.Write(fds[1], []byte{0x10, 0xff, 0x81, 0, 0, 0, 0}); err != nil {
				t.Fatal(err)
			}
			readReport(t, r, []byte{0x10, 0xff, 0x81, 0, 0, 0, 0})
			if fail {
				if _, err := unix.Write(fds[1], []byte{2, 0, 0, 0, 0, 0, 0, 0}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-u.receiverControl.done:
				case <-time.After(time.Second):
					t.Fatal("malformed input did not stop worker")
				}
				if u.ReceiverControlError() == nil {
					t.Fatal("lost worker failure")
				}
			}
			done := make(chan struct{})
			go func() { u.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Close did not join reader")
			}
			if _, err := unix.Read(fds[0], make([]byte, 20)); !errors.Is(err, unix.EBADF) {
				t.Fatalf("reader fd not closed: %v", err)
			}
		})
	}
}

func TestReceiverControlRebindFreshFlagsAndNoOldGeneration(t *testing.T) {
	u := receiverTestGadget(t)
	u.udc = "synthetic.usb"
	u.receiverControl.owned = true
	r, w := reportPipe(t)
	u.relMouseHidFile = w
	var peers []int
	opened := make(chan int, 3)
	u.receiverControl.openReader = func(string, int) (int, error) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
		if err != nil {
			return 0, err
		}
		if err := unix.SetNonblock(fds[0], true); err != nil {
			return 0, err
		}
		opened <- fds[1]
		return fds[0], nil
	}
	defer func() {
		u.Close()
		for _, fd := range peers {
			unix.Close(fd)
		}
	}()
	if err := u.startReceiverControl(); err != nil {
		t.Fatal(err)
	}
	select {
	case peer := <-opened:
		peers = append(peers, peer)
	case <-time.After(time.Second):
		t.Fatal("reader did not open")
	}
	if _, err := unix.Write(peers[0], []byte{0x10, 0xff, 0x80, 0, 0, 9, 0}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0x10, 0xff, 0x80, 0, 0, 0, 0})
	oldDone := u.receiverControl.done
	oldGeneration := u.receiverControl.generation
	if err := u.rebindUsbWith(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old reader not retired")
	}
	select {
	case peer := <-opened:
		peers = append(peers, peer)
	case <-time.After(time.Second):
		t.Fatal("new reader did not open")
	}
	if len(peers) != 2 {
		t.Fatalf("reader generations=%d", len(peers))
	}
	if _, err := unix.Write(peers[1], []byte{0x10, 0xff, 0x81, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0x10, 0xff, 0x81, 0, 0, 0, 0})
	if err := u.writeReceiverReply(context.Background(), oldGeneration, []byte{0x10, 0xff, 0x81, 0, 0, 9, 0}); !errors.Is(err, context.Canceled) {
		t.Fatal("old generation survived rebind")
	}
}

// A UDC bind precedes SetConfiguration: initial reader failure is expected,
// followed by a fresh safe minor lookup when the transport becomes online.
func TestReceiverControlInitialOfflineThenOnline(t *testing.T) {
	u := receiverTestGadget(t)
	u.udc = "synthetic.usb"
	u.receiverControl.owned = true
	r, w := reportPipe(t)
	u.relMouseHidFile = w
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	unix.SetNonblock(fds[0], true)
	defer unix.Close(fds[1])
	attempts := make(chan string, 4)
	var count atomic.Int32
	u.receiverControl.openReader = func(path string, _ int) (int, error) {
		attempts <- path
		if count.Add(1) == 1 {
			return -1, unix.ENXIO
		}
		return fds[0], nil
	}
	if err := u.startReceiverControl(); err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	select {
	case path := <-attempts:
		if path != "/dev/hidg7" {
			t.Fatal(path)
		}
	case <-time.After(time.Second):
		t.Fatal("no initial attempt")
	}
	if err := os.WriteFile(filepath.Join(u.kvmGadgetPath, "functions", "hid.usb2", "dev"), []byte("240:8"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-attempts:
		if path != "/dev/hidg8" {
			t.Fatalf("stale minor %s", path)
		}
	case <-time.After(time.Second):
		t.Fatal("no online retry")
	}
	if _, err := unix.Write(fds[1], []byte{0x10, 0xff, 0x81, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0x10, 0xff, 0x81, 0, 0, 0, 0})
	if _, err := unix.Write(fds[1], []byte{0x10, 0xff, 0x80, 0, 0, 9, 0}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0x10, 0xff, 0x80, 0, 0, 0, 0})
	if _, err := unix.Write(fds[1], []byte{0x10, 0xff, 0x80, 2, 2, 0, 0}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0x10, 0xff, 0x80, 2, 0, 0, 0})
	readReport(t, r, []byte{0x10, 1, 0x41, 0x0a, 0x11, 1, 0x40})
	readReport(t, r, []byte{0x10, 2, 0x41, 0x0a, 0x12, 2, 0x40})
	if u.ReceiverControlError() != nil {
		t.Fatal(u.ReceiverControlError())
	}
}

func TestReceiverControlRepeatedOfflineBoundedRetryAndClose(t *testing.T) {
	u := receiverTestGadget(t)
	u.udc = "synthetic.usb"
	u.receiverControl.owned = true
	var count atomic.Int32
	first := make(chan struct{})
	u.receiverControl.openReader = func(string, int) (int, error) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			return -1, err
		}
		unix.SetNonblock(fds[0], true)
		unix.Close(fds[1]) // genuine poll HUP
		if count.Add(1) == 1 {
			close(first)
		}
		return fds[0], nil
	}
	if err := u.startReceiverControl(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("no positive control open")
	}
	time.Sleep(250 * time.Millisecond)
	if got := count.Load(); got < 2 || got > 4 {
		t.Fatalf("retry attempts=%d, expected bounded 100ms retry", got)
	}
	if u.ReceiverControlError() != nil {
		t.Fatal("expected offline marked fatal")
	}
	start := time.Now()
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("Close did not interrupt backoff")
	}
	before := count.Load()
	time.Sleep(120 * time.Millisecond)
	if count.Load() != before {
		t.Fatal("retry survived Close")
	}
}

func TestReceiverControlRecoveryRebindResetKeyboardReopen(t *testing.T) {
	u := receiverTestGadget(t)
	u.udc = "synthetic.usb"
	u.receiverControl.owned = true
	opened := make(chan int, 3)
	var peers []int
	u.receiverControl.openReader = func(string, int) (int, error) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
		if err != nil {
			return -1, err
		}
		if err := unix.SetNonblock(fds[0], true); err != nil {
			return -1, err
		}
		opened <- fds[1]
		return fds[0], nil
	}
	defer func() {
		u.Close()
		for _, fd := range peers {
			unix.Close(fd)
		}
	}()
	if err := u.startReceiverControl(); err != nil {
		t.Fatal(err)
	}
	select {
	case fd := <-opened:
		peers = append(peers, fd)
	case <-time.After(time.Second):
		t.Fatal("initial worker missing")
	}
	if err := u.rebindUsbWith(func() error { u.ResetHIDFiles(); return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case fd := <-opened:
		peers = append(peers, fd)
	case <-time.After(time.Second):
		t.Fatal("rebound worker missing")
	}
	// The existing public recovery caller resets caches after a successful rebind,
	// then returns immediately when keyboard reopen succeeds.
	u.ResetHIDFiles()
	r, w := reportPipe(t)
	keyboard := hidTestSocket(t)
	u.hidOpenFile = func(path string, _ int, _ os.FileMode) (*os.File, error) {
		if path == "/dev/hidg4" {
			return keyboard, nil
		}
		if path == "/dev/hidg7" {
			return w, nil
		}
		return nil, os.ErrNotExist
	}
	if err := u.ReopenKeyboardHidFile(); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Write(peers[1], []byte{0x10, 0xff, 0x81, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	readReport(t, r, []byte{0x10, 0xff, 0x81, 0, 0, 0, 0})
}

func TestReceiverControlOldReaderCannotConsumeNewEpochQueue(t *testing.T) {
	u := receiverTestGadget(t)
	u.udc = "synthetic.usb"
	u.receiverControl.owned = true
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	var opens atomic.Int32
	allowNew := make(chan struct{})
	newAllowed := false
	u.receiverControl.openReader = func(string, int) (int, error) {
		if opens.Add(1) > 1 {
			<-allowNew
		}
		return unix.Dup(fds[0])
	}
	paused := make(chan struct{})
	resume := make(chan struct{})
	resumed := false
	var reads atomic.Int32
	u.receiverControl.beforeRead = func() {
		if reads.Add(1) == 1 {
			close(paused)
			<-resume
		}
	}
	defer func() {
		if !resumed {
			close(resume)
		}
		if !newAllowed {
			close(allowNew)
		}
		u.Close()
	}()
	r, w := reportPipe(t)
	u.relMouseHidFile = w
	if err := u.startReceiverControl(); err != nil {
		t.Fatal(err)
	}
	oldDone := u.receiverControl.done
	if _, err := unix.Write(fds[1], []byte{0x10, 0xff, 0x81, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("old reader did not reach pre-read barrier")
	}
	u.receiverControl.mu.Lock()
	cancel := u.receiverControl.cancel
	u.receiverControl.cancel = func() { cancel(); close(resume); resumed = true }
	u.receiverControl.mu.Unlock()
	if err := u.rebindUsbWith(func() error {
		// Same underlying queue/reader endpoint, just like f_hid across USB reset.
		// Reset clears the old epoch; it does not create a separate queue for old FD.
		_, err := unix.Read(fds[0], make([]byte, 20))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Write(fds[1], []byte{0x10, 0xff, 0x81, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old reader failed to retire")
	}
	close(allowNew)
	newAllowed = true
	// Before the fix the old reader consumed this new-epoch query, then rejected
	// its own stale reply. A correct new reader must still see and serve it.
	readReport(t, r, []byte{0x10, 0xff, 0x81, 0, 0, 0, 0})
}

// Cancellation releases a paused old reader. The old implementation takes the
// exclusive lifecycle lock first, so the reader cannot reach Close before bind.
func TestReceiverControlRebindClosesPausedReaderBeforeController(t *testing.T) {
	u := receiverTestGadget(t)
	u.udc = "synthetic.usb"
	u.receiverControl.owned = true
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	paused, resume := make(chan struct{}), make(chan struct{})
	var release atomic.Bool
	unblock := func() {
		if release.CompareAndSwap(false, true) {
			close(resume)
		}
	}
	u.receiverControl.openReader = func(string, int) (int, error) { return fds[0], nil }
	u.receiverControl.beforeRead = func() { close(paused); <-resume }
	if err := u.startReceiverControl(); err != nil {
		t.Fatal(err)
	}
	defer func() { unblock(); u.Close() }()
	if _, err := unix.Write(fds[1], []byte{0x10, 0xff, 0x81, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("reader did not pause")
	}
	u.receiverControl.mu.Lock()
	cancel := u.receiverControl.cancel
	u.receiverControl.cancel = func() { cancel(); unblock() }
	u.receiverControl.mu.Unlock()
	sentinel := errors.New("stop after controller assertion")
	err = u.rebindUsbWith(func() error {
		if _, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Errorf("old reader FD remains open inside controller callback: %v", err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

// The syscall admission has already passed its generation check, but has not
// returned its FD. Drain must wait for the opener to return and close it too.
func TestReceiverControlRebindDrainsLateOpenBeforeController(t *testing.T) {
	u := receiverTestGadget(t)
	u.udc = "synthetic.usb"
	u.receiverControl.owned = true
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	opened, resume := make(chan struct{}), make(chan struct{})
	var release atomic.Bool
	unblock := func() {
		if release.CompareAndSwap(false, true) {
			close(resume)
		}
	}
	u.receiverControl.openReader = func(string, int) (int, error) {
		close(opened)
		<-resume
		// Park the syscall continuation behind lifecycle admission. A rebind that
		// already holds exclusive admission cannot drain this still-owned open.
		u.hidLifecycle.RLock()
		u.hidLifecycle.RUnlock()
		return fds[0], nil
	}
	if err := u.startReceiverControl(); err != nil {
		t.Fatal(err)
	}
	defer func() { unblock(); u.Close() }()
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("open did not pause")
	}
	u.receiverControl.mu.Lock()
	cancel := u.receiverControl.cancel
	u.receiverControl.cancel = func() { cancel(); unblock() }
	u.receiverControl.mu.Unlock()
	sentinel := errors.New("stop after controller assertion")
	err = u.rebindUsbWith(func() error {
		if _, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Errorf("late-open reader FD remains open inside controller callback: %v", err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestReceiverControlDrainTimeoutAbortsRebindAndCanRetry(t *testing.T) {
	u := receiverTestGadget(t)
	u.udc = "synthetic.usb"
	u.receiverControl.owned = true
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	opened, resume := make(chan struct{}), make(chan struct{})
	var release atomic.Bool
	unblock := func() {
		if release.CompareAndSwap(false, true) {
			close(resume)
		}
	}
	u.receiverControl.openReader = func(string, int) (int, error) { close(opened); <-resume; return fds[0], nil }
	if err := u.startReceiverControl(); err != nil {
		t.Fatal(err)
	}
	defer func() { unblock(); u.Close() }()
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("open did not pause")
	}
	called := false
	start := time.Now()
	err = u.rebindUsbWith(func() error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("drain timeout: callback=%v error=%v", called, err)
	}
	if time.Since(start) > hidOpenDrainTimeout+time.Second {
		t.Fatal("drain exceeded its bound")
	}
	if err := u.startReceiverControl(); err == nil {
		t.Fatal("replacement reader admitted before retiring open returned")
	}
	oldDone := u.receiverControl.done
	unblock()
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("timed-out reader did not clean up")
	}
	sentinel := errors.New("stop after controller assertion")
	err = u.rebindUsbWith(func() error {
		called = true
		if _, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Errorf("late-open FD survived retry: %v", err)
		}
		return sentinel
	})
	if !called || !errors.Is(err, sentinel) {
		t.Fatalf("retry: callback=%v error=%v", called, err)
	}
}
