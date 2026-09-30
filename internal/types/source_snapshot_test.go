package types

import "testing"

func TestSourceWikiDeliveryIDIsStablePerPublicationGeneration(t *testing.T) {
	const eventID = "published-event"
	if got := SourceWikiDeliveryID(eventID, 7); got != "published-event:g7" {
		t.Fatalf("delivery identity = %q, want %q", got, "published-event:g7")
	}
	if got := SourceWikiDeliveryID(eventID, 8); got != "published-event:g8" {
		t.Fatalf("different generations must have distinct delivery identities; got %q", got)
	}
	if got := SourceWikiDeliveryID(eventID, 7); got != "published-event:g7" {
		t.Fatalf("repeated generation must retain its delivery identity; got %q", got)
	}
	if SourceWikiDeliveryID("", 7) != "" || SourceWikiDeliveryID(eventID, 0) != "" {
		t.Fatal("delivery identity requires an event ID and positive config generation")
	}
}
