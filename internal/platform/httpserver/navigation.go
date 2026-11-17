package httpserver

import "context"

type NavigationLink struct {
	Prefix, Label, URL string
	Active             bool
}

// Navigation contains presentation data only. Route/service authorization is
// always checked separately, irrespective of the links visible to the user.
type Navigation struct {
	UnreadRoleNotifications                            int
	Authenticated, LoginEnabled, CanUseApplications    bool
	CanReviewApplications, CanAssignApplications       bool
	CanReadUsers                                       bool
	CanReadStatistics, CanExportRegistry, CanReadAudit bool
	DisplayName, CSRFToken, ExpiresAt, CurrentPath     string
	Roles                                              []string
	Dormitories                                        []NavigationLink
}

type navigationKey struct{}

func WithNavigation(ctx context.Context, nav Navigation) context.Context {
	return context.WithValue(ctx, navigationKey{}, nav)
}

func navigationFromContext(ctx context.Context) Navigation {
	nav, _ := ctx.Value(navigationKey{}).(Navigation)
	return nav
}
