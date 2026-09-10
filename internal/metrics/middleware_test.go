package metrics

import (
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsPreservesWebSocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	s := httptest.NewServer(Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		if err := c.WriteMessage(websocket.TextMessage, []byte("terminal-ready")); err != nil {
			t.Error(err)
		}
	})))
	defer s.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, b, err := c.ReadMessage()
	if err != nil || string(b) != "terminal-ready" {
		t.Fatalf("body=%q err=%v", b, err)
	}
}
