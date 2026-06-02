package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-github/v66/github"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// botMarker stamps bot-authored content so ParseEvent can flag self-events.
const botMarker = "<!-- wazir -->"

// ErrNotProvisioned means the configured board has no cached identity yet.
var ErrNotProvisioned = errors.New("board/github: board not provisioned")

// GitHubBoard implements board.Board against GitHub Projects v2.
type GitHubBoard struct {
	api   projectsAPI
	rest  *github.Client
	store store.Store

	owner         string
	ownerType     string
	projectNumber int
	boardName     string
	botLogin      string
	repos         []string // allow-list ("owner/name")

	cached *store.BoardRecord // lazily loaded board identity
}

// EnsureProvisioned creates (if spec.Create) and/or reconciles the board's
// Status columns additively, then caches the resulting ids.
func (b *GitHubBoard) EnsureProvisioned(ctx context.Context, spec board.BoardSpec) error {
	info, found, err := b.api.GetProject(ctx, b.ownerType, b.owner, b.projectNumber)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	if !found {
		if !spec.Create {
			return fmt.Errorf("%w: project %d not found for %s", ErrNotProvisioned, b.projectNumber, b.owner)
		}
		ownerID, err := b.api.OwnerID(ctx, b.ownerType, b.owner)
		if err != nil {
			return fmt.Errorf("owner id: %w", err)
		}
		info, err = b.api.CreateProject(ctx, ownerID, spec.Name)
		if err != nil {
			return fmt.Errorf("create project: %w", err)
		}
	}

	merged, changed := mergeStatusOptions(info.Options, spec.Columns)
	if changed {
		if err := b.api.UpdateStatusOptions(ctx, info.StatusFieldID, merged); err != nil {
			return fmt.Errorf("update status options: %w", err)
		}
		info, err = b.api.GetProjectByID(ctx, info.ProjectID)
		if err != nil {
			return fmt.Errorf("re-read project: %w", err)
		}
	}

	rec := store.BoardRecord{
		ProjectNumber: info.Number,
		ProjectNodeID: info.ProjectID,
		StatusFieldID: info.StatusFieldID,
		Options:       map[string]string{},
		Owner:         b.owner,
		OwnerType:     b.ownerType,
	}
	byName := map[string]string{}
	for _, o := range info.Options {
		byName[o.Name] = o.ID
	}
	for _, p := range spec.Columns {
		if id, ok := byName[columnName(p)]; ok {
			rec.Options[string(p)] = id
		}
	}
	if err := b.store.PutBoard(info.ProjectID, rec); err != nil {
		return fmt.Errorf("cache board: %w", err)
	}
	b.cached = &rec
	return nil
}

// board lazily loads the cached board identity (single board, v1).
func (b *GitHubBoard) board(ctx context.Context) (store.BoardRecord, error) {
	if b.cached != nil {
		return *b.cached, nil
	}
	info, found, err := b.api.GetProject(ctx, b.ownerType, b.owner, b.projectNumber)
	if err != nil {
		return store.BoardRecord{}, err
	}
	if !found {
		return store.BoardRecord{}, ErrNotProvisioned
	}
	rec, ok, err := b.store.GetBoard(info.ProjectID)
	if err != nil {
		return store.BoardRecord{}, err
	}
	if !ok {
		return store.BoardRecord{}, ErrNotProvisioned
	}
	b.cached = &rec
	return rec, nil
}
