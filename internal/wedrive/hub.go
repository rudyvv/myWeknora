package wedrive

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	maxControlFrame = 768 << 10
	writeTimeout    = 10 * time.Second
	pongTimeout     = 70 * time.Second
	pingInterval    = 30 * time.Second
)

var (
	agentFrameTypes = map[string]bool{"status": true, "login_qr": true, "login_result": true, "folder_selected": true, "scan_progress": true, "scan_result": true, "error": true}
	webFrameTypes   = map[string]bool{"login": true, "select_folder": true, "scan": true, "cancel": true, "shutdown": true}
)

type controlFrame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type peer struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

func (p *peer) close() {
	p.once.Do(func() {
		close(p.done)
		_ = p.conn.Close()
	})
}

type ticket struct {
	DeviceID       string
	CanTriggerScan bool
	Expires        time.Time
}

// Hub relays a deliberately tiny control protocol. Login QR frames live only
// in process memory while a browser is connected; cookies and browser profile
// data are not accepted by the protocol and therefore cannot be persisted.
type Hub struct {
	mu       sync.RWMutex
	agents   map[string]*peer
	browsers map[string]map[*peer]struct{}
	tickets  map[string]ticket
}

func NewHub() *Hub {
	return &Hub{agents: map[string]*peer{}, browsers: map[string]map[*peer]struct{}{}, tickets: map[string]ticket{}}
}

// IsAgentOnline answers from the live WebSocket registry rather than the
// persisted device status. The latter is an audit trail of the last signed
// request and must not be presented as a current connection state.
func (h *Hub) IsAgentOnline(deviceID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.agents[deviceID] != nil
}

func (h *Hub) MintBrowserTicket(deviceID string, canTriggerScan bool) (string, time.Time, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().UTC().Add(time.Minute)
	h.mu.Lock()
	for key, item := range h.tickets {
		if time.Now().UTC().After(item.Expires) {
			delete(h.tickets, key)
		}
	}
	h.tickets[value] = ticket{DeviceID: deviceID, CanTriggerScan: canTriggerScan, Expires: expires}
	h.mu.Unlock()
	return value, expires, nil
}

func (h *Hub) ConsumeBrowserTicket(value string) (ticket, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	item, ok := h.tickets[value]
	delete(h.tickets, value)
	return item, ok && time.Now().UTC().Before(item.Expires)
}

func (h *Hub) ServeAgent(w http.ResponseWriter, r *http.Request, deviceID string) error {
	conn, err := websocket.Upgrade(w, r, nil, 16<<10, maxControlFrame)
	if err != nil {
		return err
	}
	p := newPeer(conn)
	h.mu.Lock()
	previous := h.agents[deviceID]
	h.agents[deviceID] = p
	h.mu.Unlock()
	if previous != nil {
		previous.close()
	}
	defer func() {
		h.disconnectAgent(deviceID, p)
		p.close()
	}()
	h.broadcast(deviceID, mustFrame("status", map[string]string{"state": "online"}))
	return h.readLoop(p, agentFrameTypes, func(payload []byte) { h.broadcast(deviceID, payload) })
}

func (h *Hub) disconnectAgent(deviceID string, disconnected *peer) {
	h.mu.Lock()
	removed := h.agents[deviceID] == disconnected
	if removed {
		delete(h.agents, deviceID)
	}
	h.mu.Unlock()
	if removed {
		h.broadcast(deviceID, mustFrame("status", map[string]string{"state": "offline"}))
	}
}

func (h *Hub) ServeBrowser(w http.ResponseWriter, r *http.Request, deviceID string, canTriggerScan bool) error {
	conn, err := websocket.Upgrade(w, r, nil, 16<<10, maxControlFrame)
	if err != nil {
		return err
	}
	p := newPeer(conn)
	h.mu.Lock()
	if h.browsers[deviceID] == nil {
		h.browsers[deviceID] = map[*peer]struct{}{}
	}
	h.browsers[deviceID][p] = struct{}{}
	agentOnline := h.agents[deviceID] != nil
	h.mu.Unlock()
	p.send <- mustFrame("status", map[string]interface{}{"state": map[bool]string{true: "online", false: "offline"}[agentOnline]})
	defer func() {
		h.mu.Lock()
		delete(h.browsers[deviceID], p)
		if len(h.browsers[deviceID]) == 0 {
			delete(h.browsers, deviceID)
		}
		h.mu.Unlock()
		p.close()
	}()
	return h.readLoop(p, webFrameTypes, func(payload []byte) {
		if !canTriggerScan {
			var frame controlFrame
			if json.Unmarshal(payload, &frame) != nil || frame.Type == "scan" || frame.Type == "shutdown" {
				return
			}
		}
		h.mu.RLock()
		agent := h.agents[deviceID]
		h.mu.RUnlock()
		if agent != nil {
			nonBlockingSend(agent, payload)
		}
	})
}

func newPeer(conn *websocket.Conn) *peer {
	p := &peer{conn: conn, send: make(chan []byte, 32), done: make(chan struct{})}
	conn.SetReadLimit(maxControlFrame)
	_ = conn.SetReadDeadline(time.Now().Add(pongTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongTimeout)) })
	go p.writeLoop()
	return p
}

func (h *Hub) readLoop(p *peer, allowed map[string]bool, relay func([]byte)) error {
	for {
		kind, payload, err := p.conn.ReadMessage()
		if err != nil {
			return err
		}
		if kind != websocket.TextMessage {
			return errors.New("binary WeDrive control frames are forbidden")
		}
		var frame controlFrame
		if json.Unmarshal(payload, &frame) != nil || !allowed[frame.Type] {
			return errors.New("invalid WeDrive control frame")
		}
		relay(payload)
	}
}

func (p *peer) writeLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case payload := <-p.send:
			_ = p.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if p.conn.WriteMessage(websocket.TextMessage, payload) != nil {
				p.close()
				return
			}
		case <-ticker.C:
			_ = p.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if p.conn.WriteMessage(websocket.PingMessage, nil) != nil {
				p.close()
				return
			}
		case <-p.done:
			return
		}
	}
}

func (h *Hub) broadcast(deviceID string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for browser := range h.browsers[deviceID] {
		nonBlockingSend(browser, payload)
	}
}

func nonBlockingSend(p *peer, payload []byte) {
	select {
	case p.send <- payload:
	default:
		p.close()
	}
}

func mustFrame(kind string, data interface{}) []byte {
	payload, _ := json.Marshal(map[string]interface{}{"type": kind, "data": data})
	return payload
}
