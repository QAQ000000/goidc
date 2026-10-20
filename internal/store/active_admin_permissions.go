package store

import "strings"

func ActiveAdminPermissions() []Permission {
	permissions := make([]Permission, 0, len(AdminPermissions))
	for _, permission := range AdminPermissions {
		if strings.HasPrefix(permission.Code, "credit.") || strings.HasPrefix(permission.Code, "contracts.") {
			continue
		}
		permissions = append(permissions, permission)
	}
	return permissions
}
