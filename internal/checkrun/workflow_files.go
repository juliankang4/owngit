package checkrun

import (
	"context"
	"errors"
	"strings"

	"owngit/internal/actions"
	"owngit/internal/repository"
)

// ReadActionsWorkflows reads bounded top-level workflow blobs at a pinned commit.
func ReadActionsWorkflows(ctx context.Context, pinned *repository.PinnedRepository, metadataLimit int64) ([]actions.WorkflowFile, map[string]string, error) {
	entries, err := pinned.ListTree(ctx, repository.PinnedHead, ".github/workflows", metadataLimit)
	if errors.Is(err, repository.ErrPinnedPathNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var files []actions.WorkflowFile
	oids := map[string]string{}
	var total int64
	for _, entry := range entries {
		if entry.Type == "tree" || !(strings.HasSuffix(entry.Name, ".yml") || strings.HasSuffix(entry.Name, ".yaml")) {
			continue
		}
		file := actions.WorkflowFile{Path: entry.Path, Mode: entry.Mode, Size: entry.Size}
		oids[file.Path] = entry.OID
		if len(files) < 32 && len(entry.Name) <= 100 && entry.Size >= 0 && entry.Size <= actions.MaxWorkflowBytes && entry.Size <= (1<<20)-total && (entry.Mode == "100644" || entry.Mode == "100755") {
			blob, err := pinned.ReadBlob(ctx, repository.PinnedHead, entry.Path, 0, metadataLimit, actions.MaxWorkflowBytes+1, actions.MaxWorkflowBytes+1)
			if err != nil {
				return nil, nil, err
			}
			file.Data = blob.Content
		}
		if entry.Size >= 0 && entry.Size <= actions.MaxWorkflowBytes {
			total += entry.Size
		}
		files = append(files, file)
	}
	return files, oids, nil
}
