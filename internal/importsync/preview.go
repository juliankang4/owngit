package importsync

import (
	"context"
	"sort"

	"owngit/internal/importgit"
	"owngit/internal/state"
)

// refreshEffects lists what turning on overwrite_diverged and
// follow_upstream_deletions would change now. It plans a refresh with the
// same planner a run uses, as if the source still advertised what its last
// complete refresh observed, and applies nothing. The caller holds the
// repository read lock. With no complete refresh yet there is nothing to
// compare, and the list is empty.
func (s *Service) refreshEffects(ctx context.Context, source state.ImportSource, repositoryPath string) ([]RefreshEffect, error) {
	records, err := s.Store.ImportObservations(ctx, source.RepositoryID, source.SourceGeneration)
	if err != nil {
		return nil, err
	}
	latestRunID := ""
	var head importgit.Head
	for _, record := range records {
		if record.RefName == state.ImportHeadRef {
			latestRunID = record.RunID
			head = importgit.Head{Advertised: record.OID != "", OID: record.OID, SymrefTarget: record.SymrefTarget}
		}
	}
	if latestRunID == "" {
		return nil, nil
	}
	advertisement := &importgit.Advertisement{Head: head}
	for _, record := range records {
		if record.RefName != state.ImportHeadRef && record.RunID == latestRunID {
			advertisement.Refs = append(advertisement.Refs, importgit.Ref{Name: record.RefName, OID: record.OID})
		}
	}
	selected, err := selectRefs(advertisement, source.ExtraRefPrefixes)
	if err != nil {
		return nil, err
	}
	writes, err := s.Store.RefWrites(ctx, source.RepositoryID)
	if err != nil {
		return nil, err
	}
	run := &runState{
		limits: s.effectiveLimits(), source: source, advertisement: advertisement, selected: selected, writes: writes,
		run: state.ImportRun{RepositoryID: source.RepositoryID, SourceGeneration: source.SourceGeneration},
	}
	run.source.OverwriteDiverged, run.source.FollowUpstreamDeletions = true, true
	effects, err := s.plannedEffects(ctx, run, repositoryPath)
	if problemCode(err) != CodeProtectedBranch {
		return effects, err
	}
	// Overwriting stops the whole refresh at the protected default branch.
	// Following deletions alone still deletes what it would.
	dest, _, destHEAD, err := s.readDestinationState(ctx, repositoryPath, source.ExtraRefPrefixes)
	if err != nil {
		return nil, err
	}
	protected, err := s.protectedBranch(ctx, run, repositoryPath, destHEAD)
	if err != nil {
		return nil, err
	}
	run.source.OverwriteDiverged = false
	effects, err = s.plannedEffects(ctx, run, repositoryPath)
	if err != nil {
		return nil, err
	}
	effects = append(effects, RefreshEffect{Name: protected, Effect: "refused", LocalOID: dest[protected], LocalChanged: true, History: "not_kept"})
	sort.Slice(effects, func(left, right int) bool { return effects[left].Name < effects[right].Name })
	return effects, nil
}

// plannedEffects plans run against the destination as publication does and
// returns the existing local refs the plan replaces or deletes.
func (s *Service) plannedEffects(ctx context.Context, run *runState, repositoryPath string) ([]RefreshEffect, error) {
	dest, destSymrefs, destHEAD, err := s.readDestinationState(ctx, repositoryPath, run.source.ExtraRefPrefixes)
	if err != nil {
		return nil, err
	}
	observations, err := s.observationMap(ctx, run)
	if err != nil {
		return nil, err
	}
	if err := s.inspectPublicationRefKinds(ctx, run, repositoryPath, dest, destSymrefs, destHEAD, observations); err != nil {
		return nil, err
	}
	plan, err := s.planPublication(ctx, run, repositoryPath, dest, destSymrefs, destHEAD, observations)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(run.selected.refs)+len(plan.deletedRefs))
	for _, ref := range run.selected.refs {
		names = append(names, ref.Name)
	}
	names = append(names, plan.deletedRefs...)
	sort.Strings(names)
	var effects []RefreshEffect
	for _, name := range names {
		expected, desired := plan.expected[name], plan.desired[name]
		if expected == "" || desired == expected {
			continue
		}
		effect := RefreshEffect{Name: name, Effect: "replace", LocalOID: expected, LocalChanged: true, History: "not_kept"}
		if desired == "" {
			effect.Effect = "delete"
			effect.LocalChanged = expected != observations.refs[name]
		}
		if _, branchOrTag := refKind(name); branchOrTag && plan.keepHistory {
			effect.History = "kept"
		}
		effects = append(effects, effect)
	}
	return effects, nil
}
