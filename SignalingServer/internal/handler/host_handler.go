package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"quickdesk/signaling/internal/config"
	"quickdesk/signaling/internal/middleware"
	"quickdesk/signaling/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// HostHandler implements the device-side surface under /v1/devices/*:
//   POST /v1/devices:provision                    (X-API-Key only)
//   POST /v1/devices/:device_id/heartbeat         (device_secret)
//   POST /v1/devices/:device_id/signal-tokens     (device_secret)
//   PUT  /v1/devices/:device_id/access-code       (device_secret)
//   GET  /v1/ice-config                           (X-API-Key OR device_secret)
//   POST /v1/devices/:device_id/access-code:verify (X-API-Key OR Origin-allowed)
//
// See docs §2.5, §2.6, §2.10, §2.19, §2.23.
type HostHandler struct {
	devices     *service.DeviceService
	tokens      *service.TokenService
	presence    *service.PresenceService
	settings    *service.SettingsService
	rateLimit   *service.RateLimitService
	bus         *service.EventBus
	serverSince time.Time
	appConfig   *config.Config
}

func NewHostHandler(
	devices *service.DeviceService,
	tokens *service.TokenService,
	presence *service.PresenceService,
	settings *service.SettingsService,
	rateLimit *service.RateLimitService,
	bus *service.EventBus,
	appConfig *config.Config,
) *HostHandler {
	return &HostHandler{
		devices:     devices,
		tokens:      tokens,
		presence:    presence,
		settings:    settings,
		rateLimit:   rateLimit,
		bus:         bus,
		serverSince: time.Now().UTC(),
		appConfig:   appConfig,
	}
}

// -----------------------------------------------------------------------
// POST /v1/devices:provision
// -----------------------------------------------------------------------

type provisionReq struct {
	DeviceUUID         string `json:"device_uuid" binding:"required"`
	MachineFingerprint string `json:"machine_fingerprint"`
	OS                 string `json:"os"`
	OSVersion          string `json:"os_version"`
	AppVersion         string `json:"app_version"`
}

type provisionResp struct {
	DeviceID     string `json:"device_id"`
	DeviceSecret string `json:"device_secret"` // returned exactly once
	IsNew        bool   `json:"is_new"`
}

func (h *HostHandler) Provision(c *gin.Context) {
	var req provisionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		ProblemBadRequest(c, ProblemCodeInvalidRequest, err.Error())
		return
	}
	result, err := h.devices.Provision(c.Request.Context(), service.ProvisionRequest{
		DeviceUUID:         req.DeviceUUID,
		MachineFingerprint: req.MachineFingerprint,
		OS:                 req.OS,
		OSVersion:          req.OSVersion,
		AppVersion:         req.AppVersion,
	})
	if err != nil {
		ProblemInternal(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, provisionResp{
		DeviceID:     result.DeviceID,
		DeviceSecret: result.DeviceSecret,
		IsNew:        result.IsNew,
	})
}

// -----------------------------------------------------------------------
// POST /v1/devices/:device_id/heartbeat
// -----------------------------------------------------------------------

type heartbeatReq struct {
	AppVersion string `json:"app_version"`
	OS         string `json:"os"`
	OSVersion  string `json:"os_version"`
}

type heartbeatResp struct {
	ServerTime                   string `json:"server_time"`
	TurnConfigVersion            int64  `json:"turn_config_version"`
	SuggestedHeartbeatIntervalSec int   `json:"suggested_heartbeat_interval_sec,omitempty"`
}

func (h *HostHandler) Heartbeat(c *gin.Context) {
	deviceID := middleware.MustDeviceID(c)
	// Basic 1s-floor rate limit (§2.10 footnote).
	if blocked, _ := h.rateLimit.HeartbeatThrottle(c.Request.Context(), deviceID); blocked {
		ProblemTooManyRequests(c, ProblemCodeRateLimited, "Heartbeat rate exceeded", 1)
		return
	}
	var req heartbeatReq
	_ = c.ShouldBindJSON(&req)

	if err := h.devices.Heartbeat(c.Request.Context(), deviceID, req.OS, req.OSVersion, req.AppVersion); err != nil {
		ProblemInternal(c, err.Error())
		return
	}

	// Refresh presence heartbeat TTL.
	if err := h.presence.Heartbeat(c.Request.Context(), deviceID); err != nil {
		ProblemInternal(c, "failed to refresh heartbeat presence")
		return
	}

	c.JSON(http.StatusOK, heartbeatResp{
		ServerTime:        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		TurnConfigVersion: h.settings.Get().TurnConfigVersion,
	})
}

// -----------------------------------------------------------------------
// POST /v1/devices/:device_id/signal-tokens
// -----------------------------------------------------------------------

type signalTokenResp struct {
	SignalToken string `json:"signal_token"`
	ExpiresAt   string `json:"expires_at"`
}

func (h *HostHandler) IssueHostSignalToken(c *gin.Context) {
	deviceID := middleware.MustDeviceID(c)
	if blocked, _ := h.rateLimit.SignalTokenThrottle(c.Request.Context(), deviceID); blocked {
		ProblemTooManyRequests(c, ProblemCodeRateLimited, "signal-tokens rate exceeded", 1)
		return
	}
	tok, exp, err := h.tokens.IssueSignalToken(c.Request.Context(), service.SignalTokenPayload{
		DeviceID: deviceID,
		Role:     service.SignalRoleHost,
	})
	if err != nil {
		ProblemInternal(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, signalTokenResp{
		SignalToken: tok,
		ExpiresAt:   exp.Format("2006-01-02T15:04:05Z"),
	})
}

// -----------------------------------------------------------------------
// PUT /v1/devices/:device_id/access-code  (called by Qt, §2.23)
// -----------------------------------------------------------------------

type setAccessCodeReq struct {
	AccessCode string `json:"access_code" binding:"required"`
}

func (h *HostHandler) SetAccessCode(c *gin.Context) {
	deviceID := middleware.MustDeviceID(c)
	var req setAccessCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		ProblemBadRequest(c, ProblemCodeInvalidRequest, err.Error())
		return
	}
	if err := h.devices.SetAccessCode(c.Request.Context(), deviceID, req.AccessCode); err != nil {
		ProblemInternal(c, err.Error())
		return
	}
	// Owner gets notified so their UI refreshes. Only publish to the
	// device's current owner (if any); unbound devices produce no user
	// event (webhook/audit subscribers see it anyway).
	d, err := h.devices.GetByDeviceID(c.Request.Context(), deviceID)
	if err == nil && d.UserID != nil {
		h.bus.Publish(c.Request.Context(), service.Event{
			Type:     service.EventDeviceAccessCodeChanged,
			UserID:   *d.UserID,
			DeviceID: deviceID,
			Data: map[string]interface{}{
				"device_id":           deviceID,
				"access_code_changed": true,
			},
		})
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// -----------------------------------------------------------------------
// GET /v1/ice-config
// -----------------------------------------------------------------------

type iceServerJSON struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type iceConfigResp struct {
	IceServers        []iceServerJSON `json:"ice_servers"`
	TurnConfigVersion int64           `json:"turn_config_version"`
}

// GetICEConfig generates TURN credentials against the coturn shared secret
// and returns the full ICE servers list. The endpoint is callable with
// either X-API-Key (host startup) or a valid device_secret Bearer.
func (h *HostHandler) GetICEConfig(c *gin.Context) {
	s := h.settings.Get()

	servers := make([]iceServerJSON, 0, 4)
	for _, u := range splitLines(s.StunURLs) {
		if u != "" {
			servers = append(servers, iceServerJSON{URLs: []string{u}})
		}
	}
	turnURLs := splitLines(s.TurnURLs)
	if len(turnURLs) > 0 && s.TurnAuthSecret != "" {
		user, cred := buildTurnCredential(s.TurnAuthSecret, s.TurnCredentialTTL)
		servers = append(servers, iceServerJSON{
			URLs:       turnURLs,
			Username:   user,
			Credential: cred,
		})
	}
	c.JSON(http.StatusOK, iceConfigResp{
		IceServers:        servers,
		TurnConfigVersion: s.TurnConfigVersion,
	})
}

// splitLines splits a newline/comma-delimited blob into trimmed tokens.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// buildTurnCredential implements coturn's `use-auth-secret` pattern:
//   username = "<unix_ts>:<anything>"
//   credential = base64( HMAC-SHA1(secret, username) )
func buildTurnCredential(secret string, ttlSec int) (user, cred string) {
	if ttlSec <= 0 {
		ttlSec = 86400
	}
	user = fmt.Sprintf("%d:quickdesk", time.Now().Unix()+int64(ttlSec))
	cred = hmacSHA1Base64(secret, user)
	return user, cred
}

// -----------------------------------------------------------------------
// POST /v1/devices/:device_id/access-code:verify
// -----------------------------------------------------------------------

type verifyCodeReq struct {
	Code     string `json:"code" binding:"required"`
	ClientID string `json:"client_id"`
}

type verifyCodeResp struct {
	SignalToken string `json:"signal_token"`
	ExpiresAt   string `json:"expires_at"`
}

func (h *HostHandler) VerifyAccessCode(c *gin.Context) {
	deviceID := c.Param("device_id")
	ip := c.ClientIP()

	// Rate-limit precheck (§2.10): per-IP ⇒ 429, per-device or
	// per-(device,ip) ⇒ 403 TOO_MANY_ATTEMPTS.
	if decision, _ := h.rateLimit.CheckVerifyPreflight(c.Request.Context(), deviceID, ip); decision.Blocked {
		switch decision.Kind {
		case service.VerifyBlockPerIP:
			ProblemTooManyRequests(c, ProblemCodeRateLimited, decision.Reason, decision.RetryAfterSec)
		default:
			// per-device or per-(device,ip): 403 + Retry-After (§2.10).
			if decision.RetryAfterSec > 0 {
				c.Header("Retry-After", fmt.Sprintf("%d", decision.RetryAfterSec))
			}
			ProblemForbidden(c, ProblemCodeTooManyAttempts, decision.Reason)
		}
		return
	}

	var req verifyCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		ProblemBadRequest(c, ProblemCodeInvalidRequest, err.Error())
		return
	}

	exists, matches, err := h.devices.VerifyAccessCode(c.Request.Context(), deviceID, req.Code)
	if err != nil {
		ProblemInternal(c, err.Error())
		return
	}
	if !exists {
		ProblemNotFound(c, ProblemCodeDeviceNotFound, "Device not registered")
		return
	}
	if !h.presence.IsOnline(c.Request.Context(), deviceID) {
		// Conflict per §2.2 / scenario 37.
		ProblemConflict(c, ProblemCodeHostOffline, "Host is offline")
		return
	}
	if !matches {
		if failure, _ := h.rateLimit.RecordVerifyFailure(c.Request.Context(), deviceID, ip); failure.TripsLimit {
			// Tripped a per-device error counter (not the per-IP total
			// counter), so §2.10 mandates 403 + Retry-After.
			if failure.RetryAfterSec > 0 {
				c.Header("Retry-After", fmt.Sprintf("%d", failure.RetryAfterSec))
			}
			ProblemForbidden(c, ProblemCodeTooManyAttempts, "Too many failed attempts")
			return
		}
		ProblemForbidden(c, ProblemCodeInvalidCode, "Access code is incorrect")
		return
	}

	// Code matched → reset counters and mint a single-use signal_token.
	h.rateLimit.ResetVerifyFailures(c.Request.Context(), deviceID, ip)
	tok, exp, err := h.tokens.IssueSignalToken(c.Request.Context(), service.SignalTokenPayload{
		DeviceID: deviceID,
		Role:     service.SignalRoleClient,
		ClientID: req.ClientID,
	})
	if err != nil {
		ProblemInternal(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, verifyCodeResp{
		SignalToken: tok,
		ExpiresAt:   exp.Format("2006-01-02T15:04:05Z"),
	})
}

// -----------------------------------------------------------------------
// Internal helpers
// -----------------------------------------------------------------------

// ErrDeviceRecordGone is returned when a device_secret is valid but the
// underlying row has been deleted — this is how an admin secret:rotate
// tells an offending host to re-provision (§2.17).
var ErrDeviceRecordGone = errors.New("device row removed")

// ensureDeviceExists is a tiny guard for endpoints that want a pre-check
// without pulling the whole row.
func (h *HostHandler) ensureDeviceExists(c *gin.Context, deviceID string) bool {
	if _, err := h.devices.GetByDeviceID(c.Request.Context(), deviceID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ProblemNotFound(c, ProblemCodeDeviceNotFound, "Device not registered")
		} else {
			ProblemInternal(c, err.Error())
		}
		return false
	}
	return true
}
