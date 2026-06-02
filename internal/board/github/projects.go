package github

import "context"

// projectInfo is a project's identity plus its Status field state.
type projectInfo struct {
	ProjectID     string
	Number        int
	StatusFieldID string
	Options       []statusOption
}

// issueRef is an issue's forge coordinates.
type issueRef struct {
	Repo   string // "owner/name"
	Number int
}

// listedItem is a project item joined to its issue content.
type listedItem struct {
	ItemID      string
	IssueNodeID string
	Repo        string
	Number      int
	Title       string
	Body        string
}

// projectsAPI is the narrow GraphQL seam. The githubv4 impl is in
// projects_gql.go (integration-validated); tests use a fake.
type projectsAPI interface {
	OwnerID(ctx context.Context, ownerType, login string) (string, error)
	GetProject(ctx context.Context, ownerType, login string, number int) (projectInfo, bool, error)
	GetProjectByID(ctx context.Context, projectID string) (projectInfo, error)
	CreateProject(ctx context.Context, ownerID, title string) (projectInfo, error)
	UpdateStatusOptions(ctx context.Context, fieldID string, opts []optionInput) error
	FindItem(ctx context.Context, projectID, issueNodeID string) (itemID string, found bool, err error)
	SetItemStatus(ctx context.Context, projectID, itemID, fieldID, optionID string) error
	ResolveIssue(ctx context.Context, issueNodeID string) (issueRef, error)
	ListItems(ctx context.Context, projectID, statusFieldID, optionID string) ([]listedItem, error)
}
