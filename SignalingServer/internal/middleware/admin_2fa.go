package middleware

import (
	"quickdesk/signaling/internal/httpx"
	"quickdesk/signaling/internal/service"

	"github.com/gin-gonic/gin"
)

// RequireAdmin2FAForWrites implements the §2.16 rule that "super_admin
// must enable 2FA before they can mutate anything". Any admin that hasn't
// finished TOTP enrollment is allowed to GET but blocked from any
// non-safe HTTP method with a RFC 7807 Forbidden.
//
// The check runs after AdminAuth.Required() has already populated
// ctxKeyAdminID, so we can look the admin up in one read.
func RequireAdmin2FAForWrites(adminSvc *service.AdminUserService) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Safe methods pass through unconditionally.
		switch c.Request.Method {
		case "GET", "HEAD", "OPTIONS":
			c.Next()
			return
		}
		adminID := MustAdminID(c)
		if adminID == 0 {
			// AdminAuth.Required() should have aborted already; this is
			// a defensive no-op.
			c.Next()
			return
		}
		u, err := adminSvc.GetAdminUserByID(c.Request.Context(), adminID)
		if err != nil {
			httpx.Forbidden(c, httpx.CodeForbidden, "Admin account unavailable")
			return
		}
		// Only super_admins are forced into 2FA today — regular admins
		// can toggle 2FA for themselves but aren't locked out of writes
		// if they haven't. This mirrors §2.16: "super_admin 首次登录后
		// **强制**要求启用 2FA，否则只能看不能改".
		if u.Role == "super_admin" && !u.TOTPEnabled {
			httpx.Forbidden(c, "TOTP_REQUIRED", "super_admin must enable 2FA before making changes")
			return
		}
		c.Next()
	}
}
