package usbgadget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/jetkvm/kvm/internal/receivercontrol"
	"golang.org/x/sys/unix"
)

// receiverControlState belongs to a real gadget constructor. Bare test gadgets
// never acquire an unowned goroutine. Each reset retires its responder generation.
type receiverControlState struct {
	beforeRead func() // explicit test barrier before NONBLOCK read admission
	workers    sync.WaitGroup
	openReader func(string, int) (int, error) // explicit transport boundary in tests
	mu         sync.Mutex
	owned      bool
	closed     bool
	generation uint64
	cancel     context.CancelFunc
	done       chan struct{}
	err        error
	reference  *receivercontrol.Reference // explicit reference boundary for tests
}

func (u *UsbGadget) receiverReady() (*receivercontrol.Reference, error) {
	if u.receiverControl.reference != nil {
		if err := u.receiverControl.reference.Ready(); err != nil {
			return nil, err
		}
		return u.receiverControl.reference, nil
	}
	return receivercontrol.LoadEmbedded()
}
func (u *UsbGadget) stopReceiverControl() {
	s := &u.receiverControl
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// Wait without hidLifecycle: a retiring reader may need read admission to
// observe cancellation and close its FD. The worker owns an in-flight open too,
// so completion proves both the syscall return and the eventual close occurred.
// Keep done after a timeout; later retries must drain the same retiring worker.
func (u *UsbGadget) drainReceiverControl(timeout time.Duration) error {
	s := &u.receiverControl
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("USB rebind aborted: receiver control reader did not drain within %s", timeout)
	}
}

// ReceiverControlError exposes fatal transport errors instead of silently losing
// enrollment. Cancellation and expected transport epoch resets are not failures.
func (u *UsbGadget) ReceiverControlError() error {
	s := &u.receiverControl
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (u *UsbGadget) startReceiverControl() error {
	s := &u.receiverControl
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.owned || s.closed || !u.receiverEnumerationProfile() || u.udc == "" {
		return nil
	}
	if s.cancel != nil {
		return s.err
	}
	// A failed drain leaves ownership with the retiring worker. Do not lose its
	// completion channel or create another open while it can still return an FD.
	if s.done != nil {
		select {
		case <-s.done:
		default:
			return fmt.Errorf("receiver control reader is still draining")
		}
	}
	ref, err := u.receiverReady()
	if err != nil {
		return err
	}
	handler, err := receivercontrol.New(ref)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	s.err = nil
	generation, done := s.generation, s.done
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer close(done)
		err := u.receiverControlLoop(ctx, generation, handler)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.generation == generation && err != nil {
			s.err = err
			u.log.Error().Err(err).Msg("receiver control worker stopped")
		}
	}()
	return nil
}

var errReceiverOffline = errors.New("receiver control transport is offline")

func receiverOfflineError(err error) bool {
	return errors.Is(err, errReceiverOffline) || errors.Is(err, syscall.ESHUTDOWN) || errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EAGAIN)
}

// A bound UDC is not necessarily configured yet. Offline transport epochs are
// retired and reopened after a cancellable delay, with fresh responder state.
func (u *UsbGadget) receiverControlLoop(ctx context.Context, generation uint64, h *receivercontrol.Handler) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		fd, err := u.openReceiverReader(ctx, generation)
		if err == nil {
			err = u.runReceiverControl(ctx, generation, h, fd)
			unix.Close(fd)
		}
		h.Reset()
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil
		}
		if !receiverOfflineError(err) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (u *UsbGadget) openReceiverReader(ctx context.Context, generation uint64) (int, error) {
	u.hidLifecycle.RLock()
	s := &u.receiverControl
	s.mu.Lock()
	active := ctx.Err() == nil && !s.closed && generation == s.generation && u.receiverEnumerationProfile()
	openReader := s.openReader
	s.mu.Unlock()
	if !active {
		u.hidLifecycle.RUnlock()
		return -1, context.Canceled
	}
	path, err := u.hidDevicePath("relative_mouse", "")
	u.hidLifecycle.RUnlock()
	if err != nil {
		return -1, err
	}
	// Never mutate flags on the shared input writer descriptor.
	flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC
	var fd int
	if openReader != nil {
		fd, err = openReader(path, flags)
	} else {
		fd, err = unix.Open(path, flags, 0)
	}
	if err != nil {
		return -1, fmt.Errorf("open receiver control reader: %w", err)
	}
	if ctx.Err() != nil {
		unix.Close(fd)
		return -1, context.Canceled
	}
	return fd, nil
}

func (u *UsbGadget) runReceiverControl(ctx context.Context, generation uint64, h *receivercontrol.Handler, fd int) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, 100)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if poll[0].Revents&unix.POLLNVAL != 0 {
			return fmt.Errorf("receiver reader poll failure: %#x", poll[0].Revents)
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP) != 0 {
			return errReceiverOffline
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			continue
		}
		if u.receiverControl.beforeRead != nil {
			u.receiverControl.beforeRead()
		}
		b := make([]byte, 20)
		n, err := u.readReceiverControl(ctx, generation, fd, b)
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.ESHUTDOWN) {
			return errReceiverOffline
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.EOF
		}
		replies, err := h.Handle(b[:n])
		if err != nil {
			return err
		}
		for _, reply := range replies {
			if err := u.writeReceiverReply(ctx, generation, reply); err != nil {
				return err
			}
		}
	}
}

// Admit a NONBLOCK read atomically with generation validation. Old file
// descriptors share f_hid's FIFO across reset, so protecting replies alone does
// not prevent an old reader from consuming a new generation's request.
func (u *UsbGadget) readReceiverControl(ctx context.Context, generation uint64, fd int, b []byte) (int, error) {
	u.hidLifecycle.RLock()
	defer u.hidLifecycle.RUnlock()
	s := &u.receiverControl
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || s.closed || generation != s.generation || !u.receiverEnumerationProfile() {
		return 0, context.Canceled
	}
	return unix.Read(fd, b)
}

// Take the lifecycle read lock only for a bounded existing writer operation.
// Rebind's exclusive gate and generation check prevent an old reply after reset.
func (u *UsbGadget) writeReceiverReply(ctx context.Context, generation uint64, reply []byte) error {
	if !(len(reply) == 7 && reply[0] == 0x10 || len(reply) == 20 && reply[0] == 0x11) {
		return fmt.Errorf("invalid receiver vendor reply")
	}
	u.hidLifecycle.RLock()
	defer u.hidLifecycle.RUnlock()
	s := &u.receiverControl
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || s.closed || generation != s.generation || !u.receiverEnumerationProfile() {
		return context.Canceled
	}
	u.relMouseLock.Lock()
	defer u.relMouseLock.Unlock()
	return u.relMouseWriteHidFile(reply)
}
