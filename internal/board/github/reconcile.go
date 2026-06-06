package github

import (
	"slices"

	"github.com/EmadMokhtar/wazir/internal/board"
)

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

// pruneStatusOptions produces the option set for prune mode: EXACTLY the desired
// §3 columns, in canonical (desired) order. Existing options are reused by name
// so their ids — and the cards sitting in them — are preserved; missing columns
// are created (nil id). Existing options not in the desired set are returned in
// deleted (they vanish from the sent set, i.e. GitHub removes them). changed
// reports whether the resulting set differs from existing in membership or order.
func pruneStatusOptions(existing []statusOption, desired []board.Phase) (merged []optionInput, deleted []statusOption, changed bool) {
	byName := map[string]statusOption{}
	for _, e := range existing {
		byName[e.Name] = e
	}
	desiredNames := map[string]bool{}
	for _, p := range desired {
		name := columnName(p)
		desiredNames[name] = true
		if e, ok := byName[name]; ok {
			id := e.ID
			merged = append(merged, optionInput{ID: &id, Name: name, Color: e.Color, Description: e.Description})
		} else {
			merged = append(merged, optionInput{Name: name, Color: optionColor(p), Description: ""})
		}
	}
	for _, e := range existing {
		if !desiredNames[e.Name] {
			deleted = append(deleted, e)
		}
	}

	// changed if anything is dropped, or the surviving columns differ in
	// membership/order from the canonical set we're about to send.
	var surviving []string
	for _, e := range existing {
		if desiredNames[e.Name] {
			surviving = append(surviving, e.Name)
		}
	}
	changed = len(deleted) > 0 || !slices.Equal(surviving, optionNames(merged))
	return merged, deleted, changed
}

// optionNames returns the Name of each option, in order.
func optionNames(opts []optionInput) []string {
	names := make([]string, len(opts))
	for i, o := range opts {
		names[i] = o.Name
	}
	return names
}
