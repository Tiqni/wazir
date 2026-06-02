package github

import "github.com/EmadMokhtar/wazir/internal/board"

// statusOption is an existing single-select option read from GitHub.
type statusOption struct {
	ID          string
	Name        string
	Color       string
	Description string
}

// optionInput is one entry in an updateProjectV2Field options set.
// A nil ID means "create"; a set ID means "preserve this existing option".
type optionInput struct {
	ID          *string
	Name        string
	Color       string
	Description string
}

// mergeStatusOptions produces the full desired option set for the Status
// field: every existing option preserved (by id), plus any §3 columns that
// are absent appended as new options. It never drops an existing option.
// changed reports whether any column had to be added.
func mergeStatusOptions(existing []statusOption, desired []board.Phase) (merged []optionInput, changed bool) {
	present := map[string]bool{}
	for _, e := range existing {
		id := e.ID
		merged = append(merged, optionInput{
			ID:          &id,
			Name:        e.Name,
			Color:       e.Color,
			Description: e.Description,
		})
		present[e.Name] = true
	}
	for _, p := range desired {
		name := columnName(p)
		if present[name] {
			continue
		}
		merged = append(merged, optionInput{
			Name:        name,
			Color:       optionColor(p),
			Description: "",
		})
		present[name] = true
		changed = true
	}
	return merged, changed
}
