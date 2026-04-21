package dashboard

import (
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/svkexe/platform/internal/runtime"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsResizeMsg is the JSON control message sent by the browser for resize events.
type wsResizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// getShell handles GET /dashboard/vms/{id}/shell — renders the xterm.js page.
func (d *Dashboard) getShell(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	c, err := d.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if c.Status != "running" {
		http.Error(w, "VM is not running", http.StatusBadRequest)
		return
	}

	data := d.newData(r)
	data.Container = c
	d.renderPage(w, "shell.html", data)
}

// handleWS handles GET /dashboard/vms/{id}/ws — WebSocket terminal proxy.
func (d *Dashboard) handleWS(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	c, err := d.db.GetContainerByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if c.OwnerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if c.Status != "running" {
		http.Error(w, "VM is not running", http.StatusBadRequest)
		return
	}

	shellRT, ok := d.runtime.(runtime.ShellRuntime)
	if !ok {
		http.Error(w, "shell not supported by runtime", http.StatusNotImplemented)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error for vm %s: %v", id, err)
		return
	}
	defer conn.Close()

	// Pipes: browser WS → container stdin, container stdout → browser WS.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	resizeCh := make(chan runtime.ResizeEvent, 8)
	doneCh := make(chan struct{})

	opts := runtime.ExecInteractiveOpts{
		IncusName:   c.IncusName,
		Command:     []string{"/bin/bash"},
		Stdin:       stdinR,
		Stdout:      stdoutW,
		InitialCols: 80,
		InitialRows: 24,
		Resize:      resizeCh,
		Done:        doneCh,
	}

	// Start exec in background.
	execErr := make(chan error, 1)
	go func() {
		execErr <- shellRT.ExecInteractive(r.Context(), opts)
		stdoutW.Close()
	}()

	// Browser → container stdin + resize events.
	wsDone := make(chan struct{})
	go func() {
		defer close(wsDone)
		defer stdinW.Close()
		defer close(resizeCh)
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage {
				var msg wsResizeMsg
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
					select {
					case resizeCh <- runtime.ResizeEvent{Cols: msg.Cols, Rows: msg.Rows}:
					default:
					}
					continue
				}
				if _, err := stdinW.Write(data); err != nil {
					return
				}
			} else if mt == websocket.BinaryMessage {
				if _, err := stdinW.Write(data); err != nil {
					return
				}
			}
		}
	}()

	// Container stdout → browser WS.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdoutR.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Wait until browser disconnects or exec finishes.
	select {
	case <-wsDone:
	case <-doneCh:
	case err := <-execErr:
		if err != nil {
			log.Printf("shell exec error for vm %s: %v", id, err)
		}
	}
}
