//go:build integration

package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// External-package HTTP tests need the same real source pipeline without an
// import cycle through middleware. These exports exist only in the test binary.
type SourceIntegrationFixture struct {
	Ctx          context.Context
	DB           *gorm.DB
	KBs          interfaces.KnowledgeBaseService
	KB           *types.KnowledgeBase
	Source       *types.DataSource
	Knowledge    interfaces.KnowledgeService
	Chunks       interfaces.ChunkService
	Shares       interfaces.KBShareService
	AgentShares  interfaces.AgentShareService
	Sync         func()
	DataSources  interfaces.DataSourceService
	AdvanceFiles func(map[string][]byte) string
	ForcePush    func() string
}

func NewSourceIntegrationFixture(t *testing.T, extraFiles ...map[string][]byte) *SourceIntegrationFixture {
	f := newJavaSourceFixture(t, extraFiles...)
	return &SourceIntegrationFixture{Ctx: f.ctx, DB: f.db, KBs: f.kbs.(interfaces.KnowledgeBaseService), KB: f.kb, Source: f.ds, Knowledge: f.knowledge, Chunks: f.chunks, Shares: f.shares, AgentShares: f.agentShares, Sync: func() { syncSourceFixture(t, f) }, DataSources: f.service, AdvanceFiles: f.advanceFiles, ForcePush: f.forcePush}
}
