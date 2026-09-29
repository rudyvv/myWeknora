package repository

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// enrichSFCReferences resolves only literal relative script references that
// are members of this exact snapshot and pass the caller's current file/tag
// scope. Missing and out-of-scope targets intentionally remain indistinguishable.
func enrichSFCReferences(ctx context.Context, db *gorm.DB, file *types.SourceFileView) error {
	if file == nil || !strings.EqualFold(path.Ext(file.Path), ".vue") || len(file.Symbols) == 0 {
		return nil
	}
	var symbols []types.SourceSymbol
	if err := json.Unmarshal(file.Symbols, &symbols); err != nil {
		return err
	}
	resolved := map[string]bool{}
	for index := range symbols {
		region := symbols[index].Region
		if region == nil || region.Kind != "script" || region.ExternalSource == "" || region.ExternalStatus == "rejected" {
			continue
		}
		target, ok := sourceReferencePath(file.Path, region.ExternalSource)
		if !ok || !supportedExternalScript(target) {
			region.ExternalStatus = "rejected"
			region.ResolvedPath = ""
			continue
		}
		if known, exists := resolved[target]; exists {
			if known {
				region.ExternalStatus, region.ResolvedPath = "resolved", target
			} else {
				region.ExternalStatus, region.ResolvedPath = "unchecked", ""
			}
			continue
		}
		var targetID string
		query := db.WithContext(ctx).Table("source_snapshot_members sm").
			Select("sf.id").
			Joins("JOIN source_files sf ON sf.id=sm.source_file_id AND sf.data_source_id=?", file.DataSourceID).
			Where("sm.snapshot_id=? AND sm.path=? AND sm.status='parsed'", file.SnapshotID, target).
			Where(source.SnapshotSQL(ctx, "sm.snapshot_id", "sf.data_source_id", "sf.id"))
		if err := query.Limit(1).Scan(&targetID).Error; err != nil {
			return err
		}
		found := targetID != ""
		resolved[target] = found
		if found {
			region.ExternalStatus, region.ResolvedPath = "resolved", target
		} else {
			region.ExternalStatus, region.ResolvedPath = "unchecked", ""
		}
	}
	encoded, err := json.Marshal(symbols)
	if err != nil {
		return err
	}
	file.Symbols = types.JSON(encoded)
	return nil
}

func sourceReferencePath(componentPath, reference string) (string, bool) {
	if reference == "" || strings.HasPrefix(reference, "/") || strings.ContainsAny(reference, "\\:?#[\x00]") {
		return "", false
	}
	for _, segment := range strings.Split(reference, "/") {
		if segment == ".." {
			return "", false
		}
	}
	target := path.Clean(path.Join(path.Dir(componentPath), reference))
	if target == "." || target == ".." || strings.HasPrefix(target, "../") || len(target) > 4096 {
		return "", false
	}
	return target, true
}

func supportedExternalScript(target string) bool {
	language := source.LanguageForPath(target)
	return language == "javascript" || language == "typescript" || language == "tsx"
}
