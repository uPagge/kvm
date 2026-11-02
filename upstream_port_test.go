package kvm

import (
	"testing"

	"github.com/pion/webrtc/v4"
)

// A stale queued JSON request must never reach a session-aware upstream handler.
// Removing the App9 closed-session guard revives a replaced session's mutation.
func TestUpstreamPortClosedSessionDoesNotDispatch(t *testing.T) {
	s := &Session{done: make(chan struct{}), RPCChannel: &webrtc.DataChannel{}}
	s.close()
	called := false
	const method = "upstreamPortClosedMutation"
	rpcHandlers[method] = RPCHandler{Func: func(caller *Session) error {
		called = true
		return nil
	}}
	t.Cleanup(func() { delete(rpcHandlers, method) })
	onRPCMessage(webrtc.DataChannelMessage{Data: []byte(`{"jsonrpc":"2.0","method":"upstreamPortClosedMutation","id":1}`)}, s)
	if called {
		t.Fatal("closed session dispatched a session-aware mutation")
	}
}

// Keeping the old App9 call signature would discard the calling session and
// break new upstream handlers such as video-pause ownership.
func TestUpstreamPortSessionAwareHandlerReceivesCaller(t *testing.T) {
	s := &Session{done: make(chan struct{}), RPCChannel: &webrtc.DataChannel{}}
	var got *Session
	var paused bool
	const method = "upstreamPortSessionMutation"
	rpcHandlers[method] = RPCHandler{
		Func:   func(caller *Session, value bool) error { got, paused = caller, value; return nil },
		Params: []string{"paused"},
	}
	t.Cleanup(func() { delete(rpcHandlers, method) })
	onRPCMessage(webrtc.DataChannelMessage{Data: []byte(`{"jsonrpc":"2.0","method":"upstreamPortSessionMutation","params":{"paused":true},"id":1}`)}, s)
	if got != s || !paused {
		t.Fatalf("session-aware dispatch lost caller/argument: caller=%p want=%p paused=%v", got, s, paused)
	}
}
