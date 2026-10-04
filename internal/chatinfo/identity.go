package chatinfo

// Identity describes a sender; ActorID is an association, not authorization.
type Identity struct {
	ActorID        string
	PlatformUserID string
	Nickname       string
	GroupCard      string
	DisplayName    string
}
