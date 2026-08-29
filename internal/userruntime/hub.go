package userruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

var ErrRuntimeHubClosed = errors.New("runtime hub is closed")

type RuntimeInfo struct {
	WorkspaceBase string
	Hostname      string
	OS            string
	Arch          string
	ClientVersion string
	Capabilities  []string
}

// Connection is one immutable, ready generation of a registered Runtime.
// RuntimeID is stable; ConnectionID changes on every successful reconnect.
type Connection struct {
	RuntimeID    string
	ConnectionID string
	Info         RuntimeInfo
	Client       *bridge.Client

	Close func(reason string)

	closeOnce sync.Once
}

func (c *Connection) close(reason string) {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		// Close must synchronously cancel the transport so revoke and generation
		// replacement cannot return while the old client can still issue RPCs.
		if c.Close != nil {
			c.Close(reason)
		}
		if c.Client != nil {
			_ = c.Client.Close()
		}
	})
}

type Hub struct {
	log         *slog.Logger
	mu          sync.RWMutex
	connections map[string]*Connection
	closed      bool
}

func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		log:         log.With(slog.String("component", "runtime_hub")),
		connections: make(map[string]*Connection),
	}
}

// registerGuarded atomically checks an already-ready connection at the exact
// publication boundary. The guard runs while the Hub lock is held, so an
// observed transport loss cannot race between the final check and replacing a
// healthy generation.
func (h *Hub) registerGuarded(connection *Connection, guard func() error) error {
	if h == nil || connection == nil || connection.RuntimeID == "" || connection.ConnectionID == "" || connection.Client == nil {
		return errors.New("invalid runtime connection")
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		connection.close("runtime hub is shutting down")
		return ErrRuntimeHubClosed
	}
	if guard != nil {
		if err := guard(); err != nil {
			h.mu.Unlock()
			return fmt.Errorf("runtime connection commit rejected: %w", err)
		}
	}
	old := h.connections[connection.RuntimeID]
	h.connections[connection.RuntimeID] = connection
	h.mu.Unlock()
	h.log.Info("runtime connection ready",
		slog.String("runtime_id", connection.RuntimeID),
		slog.String("connection_id", connection.ConnectionID),
	)
	if old != nil && old != connection {
		old.close("runtime connection replaced")
		h.log.Info("runtime connection replaced",
			slog.String("runtime_id", connection.RuntimeID),
			slog.String("connection_id", connection.ConnectionID),
		)
	}
	return nil
}

func (h *Hub) Get(runtimeID string) (*Connection, bool) {
	if h == nil || runtimeID == "" {
		return nil, false
	}
	h.mu.RLock()
	connection, ok := h.connections[runtimeID]
	h.mu.RUnlock()
	return connection, ok
}

func (h *Hub) Kick(runtimeID string, reason string) {
	if h == nil || runtimeID == "" {
		return
	}
	h.mu.Lock()
	connection := h.connections[runtimeID]
	delete(h.connections, runtimeID)
	h.mu.Unlock()
	if connection != nil {
		connection.close(reason)
		h.log.Info("runtime connection kicked", slog.String("runtime_id", runtimeID), slog.String("reason", reason))
	}
}

// unregister removes only the exact generation owned by the exiting handler.
func (h *Hub) unregister(runtimeID string, connection *Connection, reason string) {
	if h == nil || runtimeID == "" || connection == nil {
		return
	}
	h.mu.Lock()
	current := h.connections[runtimeID]
	if current == connection {
		delete(h.connections, runtimeID)
	}
	h.mu.Unlock()
	if current == connection {
		h.log.Info("runtime connection disconnected",
			slog.String("runtime_id", runtimeID),
			slog.String("connection_id", connection.ConnectionID),
			slog.String("reason", strings.TrimSpace(reason)),
		)
	}
}

// Shutdown atomically prevents new registrations and closes every online
// generation. Hijacked WebSockets are not closed by http.Server.Shutdown.
func (h *Hub) Shutdown(_ context.Context) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	h.closed = true
	connections := make([]*Connection, 0, len(h.connections))
	for runtimeID, connection := range h.connections {
		delete(h.connections, runtimeID)
		connections = append(connections, connection)
	}
	h.mu.Unlock()

	for _, connection := range connections {
		connection.close("server shutting down")
	}
	h.log.Info("runtime hub stopped", slog.Int("connections", len(connections)))
	return nil
}
