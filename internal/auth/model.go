package auth

import "time"

// Role is one of the four fixed roles. See docs/04-auth-rbac.md.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
	RoleLiveOnly Role = "live_only"
)

// Permission gates API routes.
type Permission string

const (
	PermLive          Permission = "live"
	PermPlayback      Permission = "playback"
	PermPTZ           Permission = "ptz"
	PermExport        Permission = "export"
	PermEvents        Permission = "events"
	PermSnapshot      Permission = "snapshot"
	PermCamerasWrite  Permission = "cameras:write"
	PermUsersWrite    Permission = "users:write"
	PermSettingsWrite Permission = "settings:write"
)

// rolePerms is the fixed permission matrix from docs/04-auth-rbac.md.
var rolePerms = map[Role]map[Permission]bool{
	RoleAdmin: {
		PermLive: true, PermPlayback: true, PermPTZ: true, PermExport: true,
		PermEvents: true, PermSnapshot: true, PermCamerasWrite: true,
		PermUsersWrite: true, PermSettingsWrite: true,
	},
	RoleOperator: {
		PermLive: true, PermPlayback: true, PermPTZ: true, PermExport: true,
		PermEvents: true, PermSnapshot: true,
	},
	RoleViewer: {
		PermLive: true, PermPlayback: true, PermEvents: true, PermSnapshot: true,
	},
	RoleLiveOnly: {
		PermLive: true,
	},
}

// Roles returns the catalog of roles and their permissions (GET /roles).
func Roles() map[string][]string {
	out := make(map[string][]string, len(rolePerms))
	for role, perms := range rolePerms {
		list := make([]string, 0, len(perms))
		for p := range perms {
			list = append(list, string(p))
		}
		out[string(role)] = list
	}
	return out
}

// ValidRole reports whether s is a known role.
func ValidRole(s string) bool {
	_, ok := rolePerms[Role(s)]
	return ok
}

// Has reports whether the role grants the permission.
func (r Role) Has(p Permission) bool {
	return rolePerms[r][p]
}

// Permissions returns the sorted permission list for the role.
func (r Role) Permissions() []string {
	perms := rolePerms[r]
	out := make([]string, 0, len(perms))
	for p, ok := range perms {
		if ok {
			out = append(out, string(p))
		}
	}
	return out
}

// User is the authenticated principal.
type User struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Email       *string    `json:"email"`
	Role        Role       `json:"role"`
	Disabled    bool       `json:"-"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`

	passwordHash string

	// ViaToken is set when the request authenticated with an API token
	// rather than a session cookie (exempt from CSRF).
	ViaToken bool `json:"-"`
}

// Public returns the API-facing representation including permissions.
func (u *User) Public() map[string]any {
	return map[string]any{
		"id":            u.ID,
		"username":      u.Username,
		"display_name":  u.DisplayName,
		"email":         u.Email,
		"role":          u.Role,
		"permissions":   u.Role.Permissions(),
		"created_at":    u.CreatedAt,
		"last_login_at": u.LastLoginAt,
	}
}
