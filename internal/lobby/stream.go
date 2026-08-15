package lobby

// Stream subscribes to one of a room's two fan-outs.
//
// It returns the frame to paint immediately, a channel of subsequent frames,
// and a cancel that must be called when the caller is finished -- an SSE
// handler's defer. The channel is never closed by the room; a caller learns the
// room has gone by selecting on Done alongside it.
//
// The returned channel is buffered. If the caller stops reading, the room
// discards frames for it rather than blocking, so a phone on a bad connection
// degrades to a lower frame rate instead of slowing the game for everybody.
func (r *Room) Stream(kind StreamKind) (initial []byte, frames <-chan []byte, cancel func(), err error) {
	// Four frames is a little over a second of slack at the opening tick rate,
	// which is enough to ride out a garbage collection or a phone switching
	// from wifi to cellular without dropping anything.
	sub := &subscriber{ch: make(chan []byte, 4)}

	reply := make(chan []byte, 1)
	if err := r.send(subscribeCmd{kind: kind, sub: sub, reply: reply}); err != nil {
		return nil, nil, nil, err
	}
	initial = <-reply

	cancel = func() {
		// Best effort: if the room has already exited there is nothing to
		// unsubscribe from, and the error is not actionable.
		_ = r.send(unsubscribeCmd{kind: kind, sub: sub})
	}
	return initial, sub.ch, cancel, nil
}

// Done is closed when the room's goroutine has exited. SSE handlers select on
// it so a collected room ends its streams instead of leaving phones staring at
// a frame that will never update.
func (r *Room) Done() <-chan struct{} { return r.done }
