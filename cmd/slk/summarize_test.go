package main

import (
	"testing"

	"github.com/gammons/slk/internal/cache"
	"github.com/gammons/slk/internal/ui/messages"
)

func TestSummarizeMessages_Empty(t *testing.T) {
	got := summarizeMessages(nil)
	if got != "count=0" {
		t.Fatalf("nil: got %q", got)
	}
	got = summarizeMessages([]messages.MessageItem{})
	if got != "count=0" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestSummarizeMessages_OldestNewest(t *testing.T) {
	items := []messages.MessageItem{
		{TS: "1700000000.000100"},
		{TS: "1700000001.000200"},
		{TS: "1700000002.000300"},
	}
	got := summarizeMessages(items)
	want := "count=3 oldest=1700000000.000100 newest=1700000002.000300"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSummarizeCachedRows_OldestNewest(t *testing.T) {
	rows := []cache.Message{
		{TS: "1700000000.000100"},
		{TS: "1700000001.000200"},
	}
	got := summarizeCachedRows(rows)
	want := "count=2 oldest=1700000000.000100 newest=1700000001.000200"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
