package ask

import (
	"context"
	"testing"
	"time"
)

// TestAnswerNoPending: answering when nothing is waiting reports false and is a
// no-op (the question was already answered, superseded, or never asked).
func TestAnswerNoPending(t *testing.T) {
	b := New()
	if b.Answer("s1", "x") {
		t.Fatal("Answer returned true with no pending question")
	}
}

// TestAskUnblocksOnAnswer: Ask blocks until Answer delivers the user's choice.
func TestAskUnblocksOnAnswer(t *testing.T) {
	b := New()
	got := make(chan string, 1)
	go func() {
		ans, err := b.Ask(context.Background(), "s1")
		if err != nil {
			t.Errorf("Ask returned error: %v", err)
		}
		got <- ans
	}()

	// Wait for the question to register before answering.
	deadline := time.Now().Add(time.Second)
	for !b.Pending("s1") {
		if time.Now().After(deadline) {
			t.Fatal("question never registered as pending")
		}
		time.Sleep(time.Millisecond)
	}
	if !b.Answer("s1", "JavaScript") {
		t.Fatal("Answer returned false for a pending question")
	}
	select {
	case ans := <-got:
		if ans != "JavaScript" {
			t.Fatalf("got %q, want %q", ans, "JavaScript")
		}
	case <-time.After(time.Second):
		t.Fatal("Ask did not unblock after Answer")
	}
	if b.Pending("s1") {
		t.Fatal("question still pending after Answer")
	}
}

// TestAskCancelCleansUp: a cancelled Ask (ask-mcp process gone) returns the ctx
// error and leaves no pending entry, so a dropped question never leaks.
func TestAskCancelCleansUp(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.Ask(ctx, "s1")
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for !b.Pending("s1") {
		if time.Now().After(deadline) {
			t.Fatal("question never registered as pending")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ask returned nil error on cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("Ask did not return after cancellation")
	}
	if b.Pending("s1") {
		t.Fatal("question still pending after cancellation")
	}
	// A late answer to a cancelled question is a no-op, not a panic/block.
	if b.Answer("s1", "late") {
		t.Fatal("Answer succeeded for a cancelled question")
	}
}
