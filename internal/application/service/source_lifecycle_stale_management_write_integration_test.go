//go:build integration

package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type t19StaleManagementWriteRepository struct {
	interfaces.DataSourceRepository
	beforeUpdate func()
}

func (r *t19StaleManagementWriteRepository) Update(ctx context.Context, ds *types.DataSource) error {
	r.beforeUpdate()
	return r.DataSourceRepository.Update(ctx, ds)
}

func TestSourceLifecycleStaleManagementWritesPreserveClearAndUnbind(t *testing.T) {
	actions := []struct {
		name string
		call func(*testing.T, *DataSourceService, *javaSourceFixture) error
	}{
		{
			name: "PUT",
			call: func(_ *testing.T, service *DataSourceService, f *javaSourceFixture) error {
				edited, err := service.GetDataSource(f.ctx, f.ds.ID)
				if err != nil {
					return err
				}
				edited.Name = "stale lifecycle race edit"
				_, err = service.UpdateDataSource(f.ctx, edited)
				return err
			},
		},
		{
			name: "credentials",
			call: func(_ *testing.T, service *DataSourceService, f *javaSourceFixture) error {
				existing, err := service.GetDataSource(f.ctx, f.ds.ID)
				if err != nil {
					return err
				}
				config, err := existing.ParseConfig()
				if err != nil {
					return err
				}
				_, err = service.UpdateDataSourceCredentials(f.ctx, f.ds.ID, config.Credentials)
				return err
			},
		},
		{
			name: "resume",
			call: func(_ *testing.T, service *DataSourceService, f *javaSourceFixture) error {
				return service.ResumeDataSource(f.ctx, f.ds.ID)
			},
		},
	}
	transitions := []struct {
		name string
		call func(*testing.T, *javaSourceFixture) error
	}{
		{
			name: "clear",
			call: func(t *testing.T, f *javaSourceFixture) error {
				_, err := t19LifecycleService(t, f).ClearSource(f.ctx, f.ds.ID, true, types.SourceCleanupScopeCurrentAndHistory)
				return err
			},
		},
		{
			name: "unbind",
			call: func(t *testing.T, f *javaSourceFixture) error {
				_, err := t19LifecycleService(t, f).UnbindDataSource(f.ctx, f.ds.ID)
				return err
			},
		},
	}

	for _, action := range actions {
		for _, transition := range transitions {
			t.Run(action.name+"_racing_"+transition.name, func(t *testing.T) {
				f := newJavaSourceFixture(t)
				ensureT19LifecycleSchema(t, f)
				syncSourceFixture(t, f)
				require.NoError(t, f.service.PauseDataSource(f.ctx, f.ds.ID))
				readCtx, releaseRead := t19BeginSourceRead(t, f, f.ds.ID)
				defer releaseRead()
				beforeRead := t19Search(t, f, readCtx)
				require.NotEmpty(t, beforeRead)
				readHit := t19HitForSource(t, beforeRead, f.ds.ID)

				original := f.service
				interleaved := *original
				writeCount := 0
				var fenceAtTransition t19StaleManagementFence
				interleaved.dsRepo = &t19StaleManagementWriteRepository{
					DataSourceRepository: original.dsRepo,
					beforeUpdate: func() {
						writeCount++
						require.Equal(t, 1, writeCount, "the public service path should issue one stale repository update")
						require.NoError(t, transition.call(t, f))
						fenceAtTransition = t19ReadStaleManagementFence(t, f)
					},
				}

				// The returned write error is intentionally not asserted: rejecting
				// the stale write or safely preserving the lifecycle state are both
				// valid outcomes. Persisted state and old-read behavior are the contract.
				_ = action.call(t, &interleaved, f)
				require.Equal(t, 1, writeCount, "the race must occur after service validation and before persistence")
				require.Equal(t, fenceAtTransition, t19ReadStaleManagementFence(t, f), "the rejected stale write must not change the lifecycle fence")

				persisted, err := original.GetDataSource(f.ctx, f.ds.ID)
				require.NoError(t, err)
				require.Equal(t, types.DataSourceStatusPaused, persisted.Status, "stale management writes must not resume the source")
				require.Equal(t, types.SourceBindingUnbound, persisted.SourceBindingState)
				config, err := persisted.ParseConfig()
				require.NoError(t, err)
				require.False(t, config.HasConfiguredCredentials(persisted.Type), "lifecycle credentials remain stripped")

				if transition.name == "clear" {
					require.False(t, persisted.SourceQueryEnabled, "clear keeps source reads revoked")
					require.NotNil(t, persisted.SourceCleanup)
					require.Equal(t, types.SourceCleanupPending, persisted.SourceCleanup.Status)
					t19RequireSearchDenied(t, f, readCtx)
					_, err = f.knowledge.GetSourceFile(readCtx, readHit.KnowledgeID)
					require.Error(t, err, "clear revokes the old source-read handle")
					_, err = f.chunks.GetChunkByIDOnly(readCtx, readHit.ID)
					require.Error(t, err, "clear revokes old source chunks")
				} else {
					require.True(t, persisted.SourceQueryEnabled, "unbind retains published source reads")
					require.Nil(t, persisted.SourceCleanup)
					retained := t19Search(t, f, readCtx)
					require.Equal(t, readHit.ID, t19HitForSource(t, retained, f.ds.ID).ID)
				}

				// A fresh metadata-only PUT remains supported after unbind/clear.
				// It must preserve the terminal source identity and stripped config.
				metadataEdit, err := original.GetDataSource(f.ctx, f.ds.ID)
				require.NoError(t, err)
				metadataEdit.Name = "metadata edit while unbound"
				updated, err := original.UpdateDataSource(f.ctx, metadataEdit)
				require.NoError(t, err)
				require.Equal(t, "metadata edit while unbound", updated.Name)
				require.Equal(t, f.ds.Type, updated.Type)
				require.Equal(t, f.ds.KnowledgeBaseID, updated.KnowledgeBaseID)
				require.Equal(t, types.SourceBindingUnbound, updated.SourceBindingState)
				require.Equal(t, types.DataSourceStatusPaused, updated.Status)
				metadataConfig, err := updated.ParseConfig()
				require.NoError(t, err)
				require.False(t, metadataConfig.HasConfiguredCredentials(updated.Type))

				// A later mode edit is not an implicit rebind. In particular, a
				// caller cannot move an irreversible unbound source to document mode
				// and then back to source mode to restore connector access.
				modeEdit, err := original.GetDataSource(f.ctx, f.ds.ID)
				require.NoError(t, err)
				t19SetContentModeForStaleWriteTest(t, modeEdit, "document")
				_, modeErr := original.UpdateDataSource(f.ctx, modeEdit)
				require.Error(t, modeErr)
				persisted, err = original.GetDataSource(f.ctx, f.ds.ID)
				require.NoError(t, err)
				require.Equal(t, types.SourceBindingUnbound, persisted.SourceBindingState)
				finalConfig, err := persisted.ParseConfig()
				require.NoError(t, err)
				require.Equal(t, "source", finalConfig.Settings["content_mode"])
				require.False(t, finalConfig.HasConfiguredCredentials(persisted.Type))
				require.Equal(t, types.DataSourceStatusPaused, persisted.Status)
				if transition.name == "clear" {
					require.False(t, persisted.SourceQueryEnabled)
				} else {
					require.True(t, persisted.SourceQueryEnabled)
				}
			})
		}
	}
}

type t19StaleManagementFence struct {
	ConfigGeneration int64 `gorm:"column:config_generation"`
	FencingToken     int64 `gorm:"column:fencing_token"`
}

func t19ReadStaleManagementFence(t *testing.T, f *javaSourceFixture) t19StaleManagementFence {
	t.Helper()
	var fence t19StaleManagementFence
	require.NoError(t, f.db.Table("source_sync_states").Select("config_generation, fencing_token").
		Where("data_source_id=?", f.ds.ID).Take(&fence).Error)
	return fence
}

func t19SetContentModeForStaleWriteTest(t *testing.T, ds *types.DataSource, mode string) {
	t.Helper()
	config, err := ds.ParseConfig()
	require.NoError(t, err)
	if config.Settings == nil {
		config.Settings = make(map[string]interface{})
	}
	config.Settings["content_mode"] = mode
	ds.Config, err = config.ToJSON()
	require.NoError(t, err)
}
