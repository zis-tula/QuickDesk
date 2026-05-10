package handler

import (
	"net/http"

	"quickdesk/signaling/internal/middleware"
	"quickdesk/signaling/internal/models"
	"quickdesk/signaling/internal/service"

	"github.com/gin-gonic/gin"
)

// AdminSettingsHandler serves /v1/admin/settings. The public slice lives
// in PublicHandler; this is the full record (TURN, SMS credentials, API
// key, etc.).
type AdminSettingsHandler struct {
	settings *service.SettingsService
	bus      *service.EventBus
	audit    *service.AuditService
}

func NewAdminSettingsHandler(s *service.SettingsService, bus *service.EventBus, audit *service.AuditService) *AdminSettingsHandler {
	return &AdminSettingsHandler{settings: s, bus: bus, audit: audit}
}

// Get handles GET /v1/admin/settings.
func (h *AdminSettingsHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, h.settings.Get())
}

type adminSettingsPatch struct {
	SiteEnabled        *bool   `json:"siteEnabled"`
	SiteName           *string `json:"siteName"`
	LoginLogo          *string `json:"loginLogo"`
	SmallLogo          *string `json:"smallLogo"`
	Favicon            *string `json:"favicon"`
	TurnURLs           *string `json:"turnUrls"`
	TurnAuthSecret     *string `json:"turnAuthSecret"`
	TurnCredentialTTL  *int    `json:"turnCredentialTtl"`
	StunURLs           *string `json:"stunUrls"`
	APIKey             *string `json:"apiKey"`
	AllowedOrigins     *string `json:"allowedOrigins"`
	AdminIPWhitelist   *string `json:"adminIpWhitelist"`
	SmsAccessKeyID     *string `json:"smsAccessKeyId"`
	SmsAccessKeySecret *string `json:"smsAccessKeySecret"`
	SmsSignName        *string `json:"smsSignName"`
	SmsTemplateCode    *string `json:"smsTemplateCode"`
}

// Update handles PUT /v1/admin/settings. Any change touching TURN/STUN
// fields bumps TurnConfigVersion so hosts pick up the new ICE config on
// their next heartbeat (§2.19).
func (h *AdminSettingsHandler) Update(c *gin.Context) {
	var p adminSettingsPatch
	if err := c.ShouldBindJSON(&p); err != nil {
		ProblemBadRequest(c, ProblemCodeInvalidRequest, err.Error())
		return
	}
	cur := h.settings.Get()
	turnChanged := false

	applyString := func(dst *string, src *string) {
		if src != nil && *src != *dst {
			*dst = *src
		}
	}

	if p.SiteEnabled != nil {
		cur.SiteEnabled = *p.SiteEnabled
	}
	applyString(&cur.SiteName, p.SiteName)
	applyString(&cur.LoginLogo, p.LoginLogo)
	applyString(&cur.SmallLogo, p.SmallLogo)
	applyString(&cur.Favicon, p.Favicon)

	if p.TurnURLs != nil && cur.TurnURLs != *p.TurnURLs {
		cur.TurnURLs = *p.TurnURLs
		turnChanged = true
	}
	if p.TurnAuthSecret != nil && cur.TurnAuthSecret != *p.TurnAuthSecret {
		cur.TurnAuthSecret = *p.TurnAuthSecret
		turnChanged = true
	}
	if p.TurnCredentialTTL != nil && cur.TurnCredentialTTL != *p.TurnCredentialTTL {
		cur.TurnCredentialTTL = *p.TurnCredentialTTL
		turnChanged = true
	}
	if p.StunURLs != nil && cur.StunURLs != *p.StunURLs {
		cur.StunURLs = *p.StunURLs
		turnChanged = true
	}

	applyString(&cur.APIKey, p.APIKey)
	applyString(&cur.AllowedOrigins, p.AllowedOrigins)
	applyString(&cur.AdminIPWhitelist, p.AdminIPWhitelist)
	applyString(&cur.SmsAccessKeyID, p.SmsAccessKeyID)
	applyString(&cur.SmsAccessKeySecret, p.SmsAccessKeySecret)
	applyString(&cur.SmsSignName, p.SmsSignName)
	applyString(&cur.SmsTemplateCode, p.SmsTemplateCode)

	if turnChanged {
		cur.TurnConfigVersion++
	}

	if err := h.settings.Save(&cur); err != nil {
		ProblemInternal(c, err.Error())
		return
	}
	if turnChanged {
		h.bus.Publish(c.Request.Context(), service.Event{
			Type: service.EventTurnConfigChang,
			Data: map[string]interface{}{"turn_config_version": cur.TurnConfigVersion},
		})
	}
	h.audit.Log(c.Request.Context(), middleware.MustAdminID(c), "", "settings.update", "settings", "", "", c.ClientIP())
	c.JSON(http.StatusOK, cur)
}

// -----------------------------------------------------------------------
// Preset (single-row config distributed to clients)
// -----------------------------------------------------------------------

// AdminPresetHandler serves /v1/admin/preset.
type AdminPresetHandler struct {
	preset *service.PresetService
	audit  *service.AuditService
}

func NewAdminPresetHandler(p *service.PresetService, a *service.AuditService) *AdminPresetHandler {
	return &AdminPresetHandler{preset: p, audit: a}
}

func (h *AdminPresetHandler) Get(c *gin.Context) {
	p, err := h.preset.GetPreset()
	if err != nil {
		ProblemInternal(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, p)
}

type adminPresetReq struct {
	Notice       string `json:"notice"`
	Links        string `json:"links"`
	MinVersion   string `json:"min_version"`
	WebclientURL string `json:"webclient_url"`
}

func (h *AdminPresetHandler) Update(c *gin.Context) {
	var req adminPresetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		ProblemBadRequest(c, ProblemCodeInvalidRequest, err.Error())
		return
	}
	p := &models.Preset{
		Notice:       req.Notice,
		Links:        req.Links,
		MinVersion:   req.MinVersion,
		WebclientURL: req.WebclientURL,
	}
	if err := h.preset.UpsertPreset(p); err != nil {
		ProblemInternal(c, err.Error())
		return
	}
	h.audit.Log(c.Request.Context(), middleware.MustAdminID(c), "", "preset.update", "preset", "", "", c.ClientIP())
	c.JSON(http.StatusOK, p)
}
