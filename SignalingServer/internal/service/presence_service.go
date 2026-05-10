package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// PresenceService manages the two Redis signals that together define
// whether a device is "online":
//
//   qd:presence:device:{id}:hb           TTL 90s, refreshed by heartbeat
//   qd:presence:device:{id}:ws:{inst}    TTL 24h, one per host signaling WS
//                                         connection; instance UUID prevents
//                                         the old-process DEL from stomping
//                                         a new process's key (§2.14)
//
// A device is online iff hb exists AND at least one ws:* key exists.
// Either signal alone isn't enough — see §2.4 rationale.
type PresenceService struct {
	rdb        *redis.Client
	instanceID string // unique per running SignalingServer process
}

// PresenceState is the composite returned by IsOnline.
type PresenceState struct {
	Heartbeat bool
	WSCount   int
	Online    bool
}

const (
	presenceHeartbeatTTL = 90 * time.Second
	presenceWSTTL        = 24 * time.Hour
)

func NewPresenceService(rdb *redis.Client, instanceID string) *PresenceService {
	return &PresenceService{rdb: rdb, instanceID: instanceID}
}

func (p *PresenceService) hbKey(deviceID string) string {
	return fmt.Sprintf("qd:presence:device:%s:hb", deviceID)
}

func (p *PresenceService) wsKey(deviceID, instance string) string {
	return fmt.Sprintf("qd:presence:device:%s:ws:%s", deviceID, instance)
}

func (p *PresenceService) wsPattern(deviceID string) string {
	return fmt.Sprintf("qd:presence:device:%s:ws:*", deviceID)
}

// Heartbeat refreshes the heartbeat TTL for deviceID. Called from
// POST /v1/devices/:id/heartbeat.
func (p *PresenceService) Heartbeat(ctx context.Context, deviceID string) error {
	return p.rdb.Set(ctx, p.hbKey(deviceID), "1", presenceHeartbeatTTL).Err()
}

// MarkWSConnected records that this instance holds a signaling WS for
// deviceID. Called when the host signaling WS auth succeeds.
func (p *PresenceService) MarkWSConnected(ctx context.Context, deviceID string) error {
	return p.rdb.Set(ctx, p.wsKey(deviceID, p.instanceID), "1", presenceWSTTL).Err()
}

// MarkWSDisconnected deletes this instance's WS presence key for deviceID.
// It's safe to call even when the key doesn't exist.
func (p *PresenceService) MarkWSDisconnected(ctx context.Context, deviceID string) error {
	return p.rdb.Del(ctx, p.wsKey(deviceID, p.instanceID)).Err()
}

// State returns a consistent snapshot of the presence signals.
func (p *PresenceService) State(ctx context.Context, deviceID string) PresenceState {
	hb := p.rdb.Exists(ctx, p.hbKey(deviceID)).Val() > 0

	var wsCount int
	iter := p.rdb.Scan(ctx, 0, p.wsPattern(deviceID), 10).Iterator()
	for iter.Next(ctx) {
		wsCount++
	}
	if err := iter.Err(); err != nil {
		log.Printf("[Presence] scan error for %s: %v", deviceID, err)
	}

	return PresenceState{
		Heartbeat: hb,
		WSCount:   wsCount,
		Online:    hb && wsCount > 0,
	}
}

// IsOnline is a convenience for State(...).Online.
func (p *PresenceService) IsOnline(ctx context.Context, deviceID string) bool {
	return p.State(ctx, deviceID).Online
}

// BulkOnline looks up multiple device IDs at once, returning a map of
// deviceID → online. Used by list endpoints like GET /v1/me/devices so we
// don't do N round-trips.
func (p *PresenceService) BulkOnline(ctx context.Context, deviceIDs []string) map[string]bool {
	out := make(map[string]bool, len(deviceIDs))
	if len(deviceIDs) == 0 {
		return out
	}
	// Pipeline the hb existence checks; WS presence has to fall back to
	// SCAN per device (no MATCH-wildcard batch in Redis). For typical
	// per-user device counts (≤100) this is acceptable.
	pipe := p.rdb.Pipeline()
	cmds := make(map[string]*redis.IntCmd, len(deviceIDs))
	for _, id := range deviceIDs {
		cmds[id] = pipe.Exists(ctx, p.hbKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[Presence] pipeline exists failed: %v", err)
	}
	for _, id := range deviceIDs {
		hb := cmds[id].Val() > 0
		if !hb {
			out[id] = false
			continue
		}
		// Need at least one ws key.
		iter := p.rdb.Scan(ctx, 0, p.wsPattern(id), 1).Iterator()
		hasWS := iter.Next(ctx)
		out[id] = hb && hasWS
	}
	return out
}
