package security

import "context"

type permissionContextKey struct{}

func WithPermission(ctx context.Context, permission *Permission) context.Context {
	return context.WithValue(ctx, permissionContextKey{}, permission)
}

func PermissionFromContext(ctx context.Context) (*Permission, bool) {
	permission, ok := ctx.Value(permissionContextKey{}).(*Permission)
	return permission, ok
}
