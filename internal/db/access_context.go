package db

import (
	"context"
	"time"
)

// ContainerAccessContext cancels a shared connection when its grant disappears.
// Owners do not need a watcher. Callers always cancel when the connection ends.
func (db *DB) ContainerAccessContext(parent context.Context, container *Container, userID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if !db.CanUseContainer(container.ID, userID) {
		cancel()
		return ctx, cancel
	}
	if container.OwnerID == userID {
		return ctx, cancel
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !db.CanUseContainer(container.ID, userID) {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
