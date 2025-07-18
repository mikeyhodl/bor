package heimdallgrpc

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	hmm "github.com/ethereum/go-ethereum/heimdall-migration-monitor"
	"github.com/ethereum/go-ethereum/log"
)

func (h *HeimdallGRPCClient) FetchMilestoneCountV2(ctx context.Context) (int64, error) {
	log.Info("Fetching milestone count")

	var err error

	// Start the timer and set the request type on the context.
	start := time.Now()
	ctx = heimdall.WithRequestType(ctx, heimdall.MilestoneCountRequest)

	// Defer the metrics call.
	defer func() {
		heimdall.SendMetrics(ctx, start, err == nil)
	}()

	res, err := h.milestoneQueryClient.GetMilestoneCount(ctx, nil)
	if err != nil {
		return 0, err
	}

	count := int64(res.GetCount())

	log.Info("Fetched milestone count", "count", count)

	return count, nil
}

func (h *HeimdallGRPCClient) FetchMilestoneV2(ctx context.Context) (*milestone.MilestoneV2, error) {
	log.Debug("Fetching milestone")

	var err error

	// Start the timer and set the request type on the context.
	start := time.Now()
	ctx = heimdall.WithRequestType(ctx, heimdall.StateSyncRequest)

	// Defer the metrics call.
	defer func() {
		heimdall.SendMetrics(ctx, start, err == nil)
	}()

	res, err := h.milestoneQueryClient.GetLatestMilestone(ctx, nil)
	if err != nil {
		return nil, err
	}

	fetchedMilestone := res.GetMilestone()

	milestone := &milestone.MilestoneV2{
		Proposer:    common.HexToAddress(fetchedMilestone.Proposer),
		StartBlock:  fetchedMilestone.StartBlock,
		EndBlock:    fetchedMilestone.EndBlock,
		Hash:        common.BytesToHash(fetchedMilestone.Hash),
		BorChainID:  fetchedMilestone.BorChainId,
		MilestoneID: fetchedMilestone.MilestoneId,
		Timestamp:   fetchedMilestone.Timestamp,
	}

	log.Debug("Fetched milestone")

	return milestone, nil
}

func (h *HeimdallGRPCClient) FetchMilestoneCount(ctx context.Context) (int64, error) {
	if hmm.IsHeimdallV2 {
		return h.FetchMilestoneCountV2(ctx)
	}

	return h.FetchMilestoneCountV1(ctx)
}
