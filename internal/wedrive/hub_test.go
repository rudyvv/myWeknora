package wedrive

import (
	"strings"
	"testing"
)

func TestDisconnectCurrentAgentBroadcastsOffline(t *testing.T) {
	hub := NewHub()
	agent := &peer{}
	browser := &peer{send: make(chan []byte, 1)}
	hub.agents["device-1"] = agent
	hub.browsers["device-1"] = map[*peer]struct{}{browser: {}}

	hub.disconnectAgent("device-1", agent)

	select {
	case payload := <-browser.send:
		if !strings.Contains(string(payload), `"state":"offline"`) {
			t.Fatalf("expected offline status, got %s", payload)
		}
	default:
		t.Fatal("expected an offline status broadcast")
	}
}

func TestDisconnectReplacedAgentDoesNotMarkNewAgentOffline(t *testing.T) {
	hub := NewHub()
	oldAgent := &peer{}
	newAgent := &peer{}
	browser := &peer{send: make(chan []byte, 1)}
	hub.agents["device-1"] = newAgent
	hub.browsers["device-1"] = map[*peer]struct{}{browser: {}}

	hub.disconnectAgent("device-1", oldAgent)

	select {
	case payload := <-browser.send:
		t.Fatalf("replaced agent must not broadcast offline: %s", payload)
	default:
	}
}

func TestBrowserMaySendShutdownToItsOwnAgent(t *testing.T) {
	if !webFrameTypes["shutdown"] {
		t.Fatal("the authenticated browser control protocol must allow shutdown")
	}
}
