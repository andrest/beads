package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// failingExportRelationStore is a classic DoltStorage stand-in that serves
// one in-scope issue (bd-export1, labelled "keep") and fails exactly the bulk
// relation loader named by fail with that loader's own sentinel (GH#6870).
type failingExportRelationStore struct {
	storage.DoltStorage
	fail string
}

// exportRelationLoaders lists the five classic bulk relation loaders, the
// sentinel each one fails with, and the wrap prefix every export path must
// put in front of it (the same prefixes uowExportSource uses).
var exportRelationLoaders = []struct {
	name       string
	sentinel   error
	wantPrefix string
}{
	{"labels", errors.New("injected labels failure"), "load labels"},
	{"dependency records", errors.New("injected dependency records failure"), "load dependency records"},
	{"comments", errors.New("injected comments failure"), "load comments"},
	{"comment counts", errors.New("injected comment counts failure"), "load comment counts"},
	{"dependency counts", errors.New("injected dependency counts failure"), "load dependency counts"},
}

func (s *failingExportRelationStore) relationErr(name string) error {
	if s.fail != name {
		return nil
	}
	for _, l := range exportRelationLoaders {
		if l.name == name {
			return l.sentinel
		}
	}
	panic("unknown relation loader " + name)
}

func (s *failingExportRelationStore) GetInfraTypes(context.Context) map[string]bool { return nil }

func (s *failingExportRelationStore) GetConfig(context.Context, string) (string, error) {
	return "", nil
}

func (s *failingExportRelationStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return []*types.Issue{{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask}}, nil
}

func (s *failingExportRelationStore) GetLabelsForIssues(context.Context, []string) (map[string][]string, error) {
	if err := s.relationErr("labels"); err != nil {
		return nil, err
	}
	return map[string][]string{"bd-export1": {"keep"}}, nil
}

func (s *failingExportRelationStore) GetDependencyRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	if err := s.relationErr("dependency records"); err != nil {
		return nil, err
	}
	return map[string][]*types.Dependency{}, nil
}

func (s *failingExportRelationStore) GetCommentsForIssues(context.Context, []string) (map[string][]*types.Comment, error) {
	if err := s.relationErr("comments"); err != nil {
		return nil, err
	}
	return map[string][]*types.Comment{}, nil
}

func (s *failingExportRelationStore) GetCommentCounts(context.Context, []string) (map[string]int, error) {
	if err := s.relationErr("comment counts"); err != nil {
		return nil, err
	}
	return map[string]int{}, nil
}

func (s *failingExportRelationStore) GetDependencyCounts(context.Context, []string) (map[string]*types.DependencyCounts, error) {
	if err := s.relationErr("dependency counts"); err != nil {
		return nil, err
	}
	return map[string]*types.DependencyCounts{}, nil
}

// assertRelationLoaderErr checks that err wraps the failing loader's sentinel
// behind its uow-aligned prefix, not merely that it mentions the loader.
func assertRelationLoaderErr(t *testing.T, path string, err, sentinel error, wantPrefix string) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("%s: err = %v, want errors.Is %v", path, err, sentinel)
	}
	if !strings.HasPrefix(err.Error(), wantPrefix+": ") {
		t.Fatalf("%s: err = %q, want prefix %q", path, err.Error(), wantPrefix+": ")
	}
}

// TestClassicExportPathsRejectRelationLoaderFailures pins GH#6870 on every
// classic export path: a failing bulk relation loader must fail the export
// instead of publishing records with that relation silently empty.
func TestClassicExportPathsRejectRelationLoaderFailures(t *testing.T) {
	for _, loader := range exportRelationLoaders {
		t.Run(loader.name, func(t *testing.T) {
			saveAndRestoreGlobals(t)
			store = &failingExportRelationStore{fail: loader.name}
			issues := []*types.Issue{{ID: "bd-export1", Title: "export me"}}

			_, err := storeExportSource{}.LoadExportRelations(t.Context(), issues)
			assertRelationLoaderErr(t, "LoadExportRelations", err, loader.sentinel, loader.wantPrefix)
		})
	}
}
