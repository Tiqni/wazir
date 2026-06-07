package github

import (
	"context"
	"fmt"

	"github.com/shurcooL/githubv4"
)

// ghProjects is the githubv4-backed projectsAPI.
type ghProjects struct {
	gql *githubv4.Client
}

// UpdateProjectV2FieldInput mirrors the GraphQL input type. Defined locally so
// the build does not depend on the installed githubv4 version exposing it.
// The exported name MUST equal the GraphQL input type for variable typing.
type UpdateProjectV2FieldInput struct {
	FieldID             githubv4.ID                    `json:"fieldId"`
	SingleSelectOptions []singleSelectFieldOptionInput `json:"singleSelectOptions"`
}

type singleSelectFieldOptionInput struct {
	ID          *githubv4.String `json:"id,omitempty"`
	Name        githubv4.String  `json:"name"`
	Color       githubv4.String  `json:"color"`
	Description githubv4.String  `json:"description"`
}

func (g *ghProjects) OwnerID(ctx context.Context, ownerType, login string) (string, error) {
	vars := map[string]any{"login": githubv4.String(login)}
	if ownerType == "org" {
		var q struct {
			Organization struct{ ID githubv4.ID } `graphql:"organization(login: $login)"`
		}
		if err := g.gql.Query(ctx, &q, vars); err != nil {
			return "", err
		}
		return fmt.Sprintf("%v", q.Organization.ID), nil
	}
	var q struct {
		User struct{ ID githubv4.ID } `graphql:"user(login: $login)"`
	}
	if err := g.gql.Query(ctx, &q, vars); err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", q.User.ID), nil
}

// statusFieldFragment is the inline fragment shared by project queries.
type statusFieldFragment struct {
	SingleSelect struct {
		ID      githubv4.ID
		Options []struct {
			ID          githubv4.String
			Name        githubv4.String
			Color       githubv4.String
			Description githubv4.String
		}
	} `graphql:"... on ProjectV2SingleSelectField"`
}

func toProjectInfo(id githubv4.ID, number githubv4.Int, f statusFieldFragment) projectInfo {
	info := projectInfo{
		ProjectID:     fmt.Sprintf("%v", id),
		Number:        int(number),
		StatusFieldID: fmt.Sprintf("%v", f.SingleSelect.ID),
	}
	for _, o := range f.SingleSelect.Options {
		info.Options = append(info.Options, statusOption{
			ID: string(o.ID), Name: string(o.Name), Color: string(o.Color), Description: string(o.Description),
		})
	}
	return info
}

func (g *ghProjects) GetProject(ctx context.Context, ownerType, login string, number int) (projectInfo, bool, error) {
	vars := map[string]any{
		"login":      githubv4.String(login),
		"number":     githubv4.Int(number),
		"statusName": githubv4.String("Status"),
	}
	if ownerType == "org" {
		var q struct {
			Organization struct {
				ProjectV2 struct {
					ID     githubv4.ID
					Number githubv4.Int
					Field  statusFieldFragment `graphql:"field(name: $statusName)"`
				} `graphql:"projectV2(number: $number)"`
			} `graphql:"organization(login: $login)"`
		}
		if err := g.gql.Query(ctx, &q, vars); err != nil {
			return projectInfo{}, false, err
		}
		if q.Organization.ProjectV2.ID == nil {
			return projectInfo{}, false, nil
		}
		return toProjectInfo(q.Organization.ProjectV2.ID, q.Organization.ProjectV2.Number, q.Organization.ProjectV2.Field), true, nil
	}
	var q struct {
		User struct {
			ProjectV2 struct {
				ID     githubv4.ID
				Number githubv4.Int
				Field  statusFieldFragment `graphql:"field(name: $statusName)"`
			} `graphql:"projectV2(number: $number)"`
		} `graphql:"user(login: $login)"`
	}
	if err := g.gql.Query(ctx, &q, vars); err != nil {
		return projectInfo{}, false, err
	}
	if q.User.ProjectV2.ID == nil {
		return projectInfo{}, false, nil
	}
	return toProjectInfo(q.User.ProjectV2.ID, q.User.ProjectV2.Number, q.User.ProjectV2.Field), true, nil
}

func (g *ghProjects) GetProjectByID(ctx context.Context, projectID string) (projectInfo, error) {
	var q struct {
		Node struct {
			Project struct {
				ID     githubv4.ID
				Number githubv4.Int
				Field  statusFieldFragment `graphql:"field(name: $statusName)"`
			} `graphql:"... on ProjectV2"`
		} `graphql:"node(id: $id)"`
	}
	vars := map[string]any{"id": githubv4.ID(projectID), "statusName": githubv4.String("Status")}
	if err := g.gql.Query(ctx, &q, vars); err != nil {
		return projectInfo{}, err
	}
	return toProjectInfo(q.Node.Project.ID, q.Node.Project.Number, q.Node.Project.Field), nil
}

func (g *ghProjects) CreateProject(ctx context.Context, ownerID, title string) (projectInfo, error) {
	var m struct {
		CreateProjectV2 struct {
			ProjectV2 struct {
				ID githubv4.ID
			} `graphql:"projectV2"`
		} `graphql:"createProjectV2(input: $input)"`
	}
	input := githubv4.CreateProjectV2Input{
		OwnerID: githubv4.ID(ownerID),
		Title:   githubv4.String(title),
	}
	if err := g.gql.Mutate(ctx, &m, input, nil); err != nil {
		return projectInfo{}, err
	}
	return g.GetProjectByID(ctx, fmt.Sprintf("%v", m.CreateProjectV2.ProjectV2.ID))
}

func (g *ghProjects) UpdateStatusOptions(ctx context.Context, fieldID string, opts []optionInput) error {
	var m struct {
		UpdateProjectV2Field struct {
			Field struct {
				Typename githubv4.String `graphql:"__typename"`
			} `graphql:"projectV2Field"`
		} `graphql:"updateProjectV2Field(input: $input)"`
	}
	var gqlOpts []singleSelectFieldOptionInput
	for _, o := range opts {
		opt := singleSelectFieldOptionInput{
			Name:        githubv4.String(o.Name),
			Color:       githubv4.String(o.Color),
			Description: githubv4.String(o.Description),
		}
		if o.ID != nil {
			id := githubv4.String(*o.ID)
			opt.ID = &id
		}
		gqlOpts = append(gqlOpts, opt)
	}
	input := UpdateProjectV2FieldInput{FieldID: githubv4.ID(fieldID), SingleSelectOptions: gqlOpts}
	return g.gql.Mutate(ctx, &m, input, nil)
}

func (g *ghProjects) FindItem(ctx context.Context, projectID, issueNodeID string) (string, bool, error) {
	var q struct {
		Node struct {
			Issue struct {
				ProjectItems struct {
					Nodes []struct {
						ID      githubv4.ID
						Project struct{ ID githubv4.ID } `graphql:"project"`
					}
				} `graphql:"projectItems(first: 20)"`
			} `graphql:"... on Issue"`
		} `graphql:"node(id: $id)"`
	}
	vars := map[string]any{"id": githubv4.ID(issueNodeID)}
	if err := g.gql.Query(ctx, &q, vars); err != nil {
		return "", false, err
	}
	for _, n := range q.Node.Issue.ProjectItems.Nodes {
		if fmt.Sprintf("%v", n.Project.ID) == projectID {
			return fmt.Sprintf("%v", n.ID), true, nil
		}
	}
	return "", false, nil
}

func (g *ghProjects) SetItemStatus(ctx context.Context, projectID, itemID, fieldID, optionID string) error {
	var m struct {
		UpdateProjectV2ItemFieldValue struct {
			Item struct{ ID githubv4.ID } `graphql:"projectV2Item"`
		} `graphql:"updateProjectV2ItemFieldValue(input: $input)"`
	}
	input := githubv4.UpdateProjectV2ItemFieldValueInput{
		ProjectID: githubv4.ID(projectID),
		ItemID:    githubv4.ID(itemID),
		FieldID:   githubv4.ID(fieldID),
		Value: githubv4.ProjectV2FieldValue{
			SingleSelectOptionID: githubv4.NewString(githubv4.String(optionID)),
		},
	}
	return g.gql.Mutate(ctx, &m, input, nil)
}

func (g *ghProjects) ResolveIssue(ctx context.Context, issueNodeID string) (issueRef, error) {
	var q struct {
		Node struct {
			Issue struct {
				Number     githubv4.Int
				Repository struct{ NameWithOwner githubv4.String } `graphql:"repository"`
			} `graphql:"... on Issue"`
		} `graphql:"node(id: $id)"`
	}
	vars := map[string]any{"id": githubv4.ID(issueNodeID)}
	if err := g.gql.Query(ctx, &q, vars); err != nil {
		return issueRef{}, err
	}
	return issueRef{Repo: string(q.Node.Issue.Repository.NameWithOwner), Number: int(q.Node.Issue.Number)}, nil
}

func (g *ghProjects) ListItems(ctx context.Context, projectID, statusFieldID, optionID string) ([]listedItem, error) {
	var q struct {
		Node struct {
			Project struct {
				Items struct {
					Nodes []struct {
						ID               githubv4.ID
						FieldValueByName struct {
							SingleSelect struct {
								OptionID githubv4.String `graphql:"optionId"`
							} `graphql:"... on ProjectV2ItemFieldSingleSelectValue"`
						} `graphql:"fieldValueByName(name: $statusName)"`
						Content struct {
							Issue struct {
								ID         githubv4.ID
								Number     githubv4.Int
								Title      githubv4.String
								Body       githubv4.String
								Repository struct{ NameWithOwner githubv4.String } `graphql:"repository"`
							} `graphql:"... on Issue"`
						} `graphql:"content"`
					}
				} `graphql:"items(first: 100)"`
			} `graphql:"... on ProjectV2"`
		} `graphql:"node(id: $id)"`
	}
	vars := map[string]any{"id": githubv4.ID(projectID), "statusName": githubv4.String("Status")}
	if err := g.gql.Query(ctx, &q, vars); err != nil {
		return nil, err
	}
	var out []listedItem
	for _, n := range q.Node.Project.Items.Nodes {
		if string(n.FieldValueByName.SingleSelect.OptionID) != optionID {
			continue
		}
		if n.Content.Issue.ID == nil {
			continue
		}
		out = append(out, listedItem{
			ItemID:      fmt.Sprintf("%v", n.ID),
			IssueNodeID: fmt.Sprintf("%v", n.Content.Issue.ID),
			Repo:        string(n.Content.Issue.Repository.NameWithOwner),
			Number:      int(n.Content.Issue.Number),
			Title:       string(n.Content.Issue.Title),
			Body:        string(n.Content.Issue.Body),
		})
	}
	return out, nil
}

// StatusOptionItemCounts counts items per Status option (keyed by option id).
// Caps at the first 100 items (v1); a column with cards only beyond that window
// could be undercounted, so prune's guard is best-effort on very large boards.
func (g *ghProjects) StatusOptionItemCounts(ctx context.Context, projectID string) (map[string]int, error) {
	var q struct {
		Node struct {
			Project struct {
				Items struct {
					Nodes []struct {
						FieldValueByName struct {
							SingleSelect struct {
								OptionID githubv4.String `graphql:"optionId"`
							} `graphql:"... on ProjectV2ItemFieldSingleSelectValue"`
						} `graphql:"fieldValueByName(name: $statusName)"`
					}
				} `graphql:"items(first: 100)"`
			} `graphql:"... on ProjectV2"`
		} `graphql:"node(id: $id)"`
	}
	vars := map[string]any{"id": githubv4.ID(projectID), "statusName": githubv4.String("Status")}
	if err := g.gql.Query(ctx, &q, vars); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, n := range q.Node.Project.Items.Nodes {
		if id := string(n.FieldValueByName.SingleSelect.OptionID); id != "" {
			counts[id]++
		}
	}
	return counts, nil
}

func (g *ghProjects) ItemStatus(ctx context.Context, projectID, issueNodeID string) (string, bool, error) {
	var q struct {
		Node struct {
			Issue struct {
				ProjectItems struct {
					Nodes []struct {
						Project          struct{ ID githubv4.ID } `graphql:"project"`
						FieldValueByName struct {
							SingleSelect struct {
								OptionID githubv4.String `graphql:"optionId"`
							} `graphql:"... on ProjectV2ItemFieldSingleSelectValue"`
						} `graphql:"fieldValueByName(name: $statusName)"`
					}
				} `graphql:"projectItems(first: 20)"`
			} `graphql:"... on Issue"`
		} `graphql:"node(id: $id)"`
	}
	vars := map[string]any{"id": githubv4.ID(issueNodeID), "statusName": githubv4.String("Status")}
	if err := g.gql.Query(ctx, &q, vars); err != nil {
		return "", false, err
	}
	for _, n := range q.Node.Issue.ProjectItems.Nodes {
		if fmt.Sprintf("%v", n.Project.ID) != projectID {
			continue
		}
		id := string(n.FieldValueByName.SingleSelect.OptionID)
		if id == "" {
			return "", false, nil // on the board but no Status set yet
		}
		return id, true, nil
	}
	return "", false, nil
}

var _ projectsAPI = (*ghProjects)(nil)
