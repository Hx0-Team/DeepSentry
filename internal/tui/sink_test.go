package tui

import (
	"testing"
	"time"

	"ai-edr/internal/harness"
)

func TestChannelSinkNeverDropsSemanticEventAndCloseUnblocks(t *testing.T) {
	sink := NewChannelSink(1)
	sink.Emit(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "fills buffer"})
	delivered := make(chan struct{})
	go func() {
		sink.Emit(harness.UIEvent{Kind: harness.EventFinish, Message: "final report"})
		close(delivered)
	}()

	select {
	case <-delivered:
		t.Fatal("semantic event unexpectedly returned while queue was full")
	case <-time.After(30 * time.Millisecond):
	}
	<-sink.Events()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("semantic event was not delivered after queue drained")
	}
	if event := <-sink.Events(); event.Kind != harness.EventFinish {
		t.Fatalf("got %s want finish", event.Kind)
	}

	sink.Emit(harness.UIEvent{Kind: harness.EventInfo, Message: "fills again"})
	blocked := make(chan struct{})
	go func() {
		sink.Emit(harness.UIEvent{Kind: harness.EventError, Message: "blocked"})
		close(blocked)
	}()
	time.Sleep(20 * time.Millisecond)
	sink.Close()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock semantic emitter")
	}
}
