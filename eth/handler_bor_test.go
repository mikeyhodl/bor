package eth

import (
	"context"
	"testing"
	"time"

	"github.com/0xPolygon/heimdall-v2/x/bor/types"
	"github.com/stretchr/testify/require"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

type mockHeimdall struct {
	fetchCheckpoint      func(ctx context.Context, number int64) (*checkpoint.Checkpoint, error)
	fetchCheckpointCount func(ctx context.Context) (int64, error)
	fetchMilestone       func(ctx context.Context) (*milestone.Milestone, error)
	fetchMilestoneCount  func(ctx context.Context) (int64, error)
}

func (m *mockHeimdall) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	return nil, nil
}

func (m *mockHeimdall) GetSpan(ctx context.Context, spanID uint64) (*types.Span, error) {
	return nil, nil
}

func (m *mockHeimdall) GetLatestSpan(ctx context.Context) (*types.Span, error) {
	return nil, nil
}

func (m *mockHeimdall) FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error) {
	return m.fetchCheckpoint(ctx, number)
}

func (m *mockHeimdall) FetchCheckpointCount(ctx context.Context) (int64, error) {
	return m.fetchCheckpointCount(ctx)
}

func (m *mockHeimdall) FetchMilestone(ctx context.Context) (*milestone.Milestone, error) {
	return m.fetchMilestone(ctx)
}

func (m *mockHeimdall) FetchMilestoneCount(ctx context.Context) (int64, error) {
	return m.fetchMilestoneCount(ctx)
}

func (m *mockHeimdall) Close() {}

func (m *mockHeimdall) FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error) {
	return &ctypes.SyncInfo{CatchingUp: false}, nil
}

func TestFetchWhitelistCheckpointAndMilestone(t *testing.T) {
	t.Parallel()

	// create a minimal blockchain for the test
	db := rawdb.NewMemoryDatabase()
	gspec := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc:  core.GenesisAlloc{},
	}
	chain, _ := core.NewBlockChain(db, gspec, ethash.NewFaker(), core.DefaultConfig())
	defer chain.Stop()

	// create a downloader with the blockchain
	mux := new(event.TypeMux)
	mockDownloader := downloader.New(db, mux, chain, nil, func(string) {}, func() {}, whitelist.NewService(db, false, 0), 0, false)

	// create an ethHandler with the mock downloader
	handler := &ethHandler{
		downloader: mockDownloader,
	}

	// create a mock checkpoint verification function and use it to create a verifier
	verify := func(ctx context.Context, eth *Ethereum, handler *ethHandler, start uint64, end uint64, hash string, isCheckpoint bool) (string, error) {
		return "", nil
	}

	verifier := newBorVerifier()
	verifier.setVerify(verify)

	// Create a mock heimdall instance and use it for creating a bor instance
	var heimdall mockHeimdall

	bor := &bor.Bor{HeimdallClient: &heimdall}

	fetchCheckpointTest(t, &heimdall, bor, handler, verifier)
	fetchMilestoneTest(t, &heimdall, bor, handler, verifier)
}

func (b *borVerifier) setVerify(verifyFn func(ctx context.Context, eth *Ethereum, handler *ethHandler, start uint64, end uint64, hash string, isCheckpoint bool) (string, error)) {
	b.verify = verifyFn
}

func fetchCheckpointTest(t *testing.T, heimdall *mockHeimdall, bor *bor.Bor, handler *ethHandler, verifier *borVerifier) {
	t.Helper()

	var checkpoints []*checkpoint.Checkpoint
	// create a mock fetch checkpoint function
	heimdall.fetchCheckpoint = func(_ context.Context, number int64) (*checkpoint.Checkpoint, error) {
		if len(checkpoints) == 0 {
			return nil, errCheckpoint
		} else if number == -1 {
			return checkpoints[len(checkpoints)-1], nil
		} else {
			return checkpoints[number-1], nil
		}
	}

	_, err := handler.fetchWhitelistCheckpoint(t.Context(), bor)
	require.ErrorIs(t, err, errCheckpoint)

	// create 4 mock checkpoints
	checkpoints = createMockCheckpoints(4)

	checkpoint, err := handler.fetchWhitelistCheckpoint(t.Context(), bor)
	blockHash, err2 := handler.handleWhitelistCheckpoint(t.Context(), checkpoint, nil, verifier, false)
	blockNum := checkpoint.EndBlock

	// Check if we have expected result
	require.Equal(t, err, nil)
	require.Equal(t, err2, nil)
	require.Equal(t, checkpoints[len(checkpoints)-1].EndBlock, blockNum)
	require.Equal(t, checkpoints[len(checkpoints)-1].RootHash, blockHash)
}

func fetchMilestoneTest(t *testing.T, heimdall *mockHeimdall, bor *bor.Bor, handler *ethHandler, verifier *borVerifier) {
	t.Helper()

	var milestones []*milestone.Milestone
	// create a mock fetch checkpoint function
	heimdall.fetchMilestone = func(_ context.Context) (*milestone.Milestone, error) {
		if len(milestones) == 0 {
			return nil, errMilestone
		} else {
			return milestones[len(milestones)-1], nil
		}
	}

	_, err := handler.fetchWhitelistMilestone(t.Context(), bor)
	require.ErrorIs(t, err, errMilestone)

	// create 4 mock checkpoints
	milestones = createMockMilestones(4)

	milestone, err := handler.fetchWhitelistMilestone(t.Context(), bor)
	num := milestone.EndBlock
	hash := milestone.Hash

	// Check if we have expected result
	require.Equal(t, err, nil)
	require.Equal(t, milestones[len(milestones)-1].EndBlock, num)
	require.Equal(t, milestones[len(milestones)-1].Hash, hash)
}

func createMockCheckpoints(count int) []*checkpoint.Checkpoint {
	var (
		checkpoints []*checkpoint.Checkpoint = make([]*checkpoint.Checkpoint, count)
		startBlock  uint64                   = 257 // any number can be used
	)

	for i := 0; i < count; i++ {
		checkpoints[i] = &checkpoint.Checkpoint{
			Proposer:   common.Address{},
			StartBlock: startBlock,
			EndBlock:   startBlock + 255,
			RootHash:   common.Hash{},
			BorChainID: "137",
			Timestamp:  uint64(time.Now().Unix()),
		}
		startBlock += 256
	}

	return checkpoints
}

func createMockMilestones(count int) []*milestone.Milestone {
	var (
		milestones []*milestone.Milestone = make([]*milestone.Milestone, count)
		startBlock uint64                 = 257 // any number can be used
	)

	for i := 0; i < count; i++ {
		milestones[i] = &milestone.Milestone{
			Proposer:   common.Address{},
			StartBlock: startBlock,
			EndBlock:   startBlock + 255,
			Hash:       common.Hash{},
			BorChainID: "137",
			Timestamp:  uint64(time.Now().Unix()),
		}
		startBlock += 256
	}

	return milestones
}
