package bor

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	lru "github.com/hashicorp/golang-lru"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
)

// maxSpanFetchLimit denotes maximum number of future spans to fetch. During snap sync,
// we verify very large batch of headers. The maximum range is not known as of now and
// hence we set a very high limit. It can be reduced later.
// const maxSpanFetchLimit = 10_000

// SpanStore acts as a simple middleware to cache span data populated from heimdall. It is used
// in multiple places of bor consensus for verification.
type SpanStore struct {
	store *lru.ARCCache

	latestSpanCache atomic.Pointer[borTypes.Span]

	heimdallClient IHeimdallClient
	spanner        Spanner

	chainId           string
	lastUsedSpan      atomic.Pointer[borTypes.Span]
	latestKnownSpanId atomic.Uint64
	heimdallStatus    atomic.Pointer[ctypes.SyncInfo]

	// cancel function to stop the background routine
	cancel context.CancelFunc
}

func NewSpanStore(heimdallClient IHeimdallClient, spanner Spanner, chainId string) *SpanStore {
	cache, _ := lru.NewARC(10)
	store := SpanStore{
		store:           cache,
		heimdallClient:  heimdallClient,
		spanner:         spanner,
		chainId:         chainId,
		latestSpanCache: atomic.Pointer[borTypes.Span]{},
		lastUsedSpan:    atomic.Pointer[borTypes.Span]{},
	}

	ctx, cancel := context.WithCancel(context.Background())

	store.cancel = cancel

	if heimdallClient != nil {
		go func() {
			errorLogInterval := 10 * time.Second
			var lastSpanErrorLogTime time.Time
			var lastHeimdallErrorLogTime time.Time

			for {
				err := store.updateLatestSpan(ctx)
				if err != nil {
					if time.Since(lastSpanErrorLogTime) >= errorLogInterval {
						log.Error("Failed to update latest span", "err", err)
						lastSpanErrorLogTime = time.Now()
					}
				}
				err = store.updateHeimdallStatus(ctx)
				if err != nil {
					if time.Since(lastHeimdallErrorLogTime) >= errorLogInterval {
						log.Error("Failed to update heimdall status", "err", err)
						lastHeimdallErrorLogTime = time.Now()
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(200 * time.Millisecond):
				}
			}
		}()
	}

	return &store
}

func (s *SpanStore) getLatestSpan(ctx context.Context) (*borTypes.Span, error) {
	if s.latestSpanCache.Load() != nil {
		return s.latestSpanCache.Load(), nil
	}

	err := s.updateLatestSpan(ctx)
	if err != nil {
		return nil, err
	}
	return s.latestSpanCache.Load(), nil
}

func (s *SpanStore) updateHeimdallStatus(ctx context.Context) (err error) {
	var syncInfo *ctypes.SyncInfo
	if s.heimdallClient == nil {
		syncInfo = &ctypes.SyncInfo{CatchingUp: false}
	} else {
		syncInfo, err = s.heimdallClient.FetchStatus(ctx)
		if err != nil {
			s.heimdallStatus.Store(nil)
			return err
		}
	}
	s.heimdallStatus.Store(syncInfo)
	return nil
}

func (s *SpanStore) waitUntilHeimdallIsSynced(ctx context.Context) {
	// If there's no heimdall client, don't wait
	if s.heimdallClient == nil {
		return
	}

	timeout := 200 * time.Millisecond
	logInterval := 10 * time.Second
	var lastLogTime time.Time

	for {
		syncInfo := s.heimdallStatus.Load()
		if syncInfo == nil || syncInfo.CatchingUp {
			if time.Since(lastLogTime) >= logInterval {
				log.Warn("Heimdall isn't synced, waiting for update", "syncInfo", syncInfo)
				lastLogTime = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(timeout):
				s.updateHeimdallStatus(ctx)
				continue
			}
		}

		return
	}
}

func (s *SpanStore) updateLatestSpan(ctx context.Context) error {
	if s.heimdallClient == nil {
		return nil
	}

	latestSpan, err := s.heimdallClient.GetLatestSpan(ctx)
	if err != nil {
		return err
	}

	validators := make([]*valset.Validator, len(latestSpan.ValidatorSet.Validators))
	for i, v := range latestSpan.ValidatorSet.Validators {
		validators[i] = &valset.Validator{
			ID:               v.ValId,
			Address:          common.HexToAddress(v.Signer),
			VotingPower:      v.VotingPower,
			ProposerPriority: v.ProposerPriority,
		}
	}

	selectedProducers := make([]*valset.Validator, len(latestSpan.SelectedProducers))
	for i, v := range latestSpan.SelectedProducers {
		selectedProducers[i] = &valset.Validator{
			ID:               v.ValId,
			Address:          common.HexToAddress(v.Signer),
			VotingPower:      v.VotingPower,
			ProposerPriority: v.ProposerPriority,
		}
	}

	s.latestSpanCache.Store(&borTypes.Span{
		Id:                latestSpan.Id,
		StartBlock:        latestSpan.StartBlock,
		EndBlock:          latestSpan.EndBlock,
		SelectedProducers: span.ConvertBorValidatorsToHeimdallValidators(selectedProducers),
		ValidatorSet:      span.ConvertBorValSetToHeimdallValSet(valset.NewValidatorSet(validators)),
		BorChainId:        s.chainId,
	})
	return nil
}

// spanById returns a span given its id. It fetches span from heimdall if not found in cache.
func (s *SpanStore) spanById(ctx context.Context, spanId uint64) (*borTypes.Span, error) {
	var currentSpan *borTypes.Span
	if value, ok := s.store.Get(spanId); ok {
		currentSpan, _ = value.(*borTypes.Span)
	}

	if currentSpan != nil {
		return currentSpan, nil
	}

	var err error
	if s.heimdallClient == nil {
		if spanId == 0 {
			currentSpan, err = getMockSpan0(ctx, s.spanner, s.chainId)
			if err != nil {
				log.Warn("Unable to fetch span from heimdall", "id", spanId, "err", err)
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("unable to create test span without heimdall client for id %d", spanId)
		}
	} else {
		currentSpan, err = s.heimdallClient.GetSpan(ctx, spanId)
		if err != nil {
			log.Warn("Unable to fetch span from heimdall", "id", spanId, "err", err)
			return nil, err
		}

		if len(currentSpan.SelectedProducers) == 0 {
			log.Warn("Span from Heimdall has empty SelectedProducers", "spanId", spanId, "selectedProducers", currentSpan.SelectedProducers, "validators", currentSpan.ValidatorSet.Validators, "startBlock", currentSpan.StartBlock, "endBlock", currentSpan.EndBlock)
			return nil, fmt.Errorf("span %d has empty SelectedProducers, possibly incomplete", spanId)
		}
	}

	if currentSpan == nil {
		return nil, fmt.Errorf("span not found for id %d", spanId)
	}

	s.store.Add(spanId, currentSpan)
	if currentSpan.Id > s.latestKnownSpanId.Load() {
		s.latestKnownSpanId.Store(currentSpan.Id)
	}

	return currentSpan, nil
}

// spanByBlockNumber returns a span given a block number. It fetches span from heimdall if not found in cache. It
// assumes that a span has been committed before (i.e. is current or past span) and returns an error if
// asked for a future span. This is safe to assume as we don't have a way to find out span id for a future block
// unless we hardcode the span length (which we don't want to).
func (s *SpanStore) spanByBlockNumber(ctx context.Context, blockNumber uint64) (res *borTypes.Span, err error) {
	s.waitUntilHeimdallIsSynced(ctx)

	// As we don't persist latest known span to db, we loose the value on restarts. This leads to multiple heimdall calls
	// which can be avoided. Hence we estimate the span id from block number which updates the latest known span id. Note
	// that we still check if the block number lies in the range of span before returning it.
	estimatedSpanId := s.estimateSpanId(blockNumber)
	defer func() {
		if res != nil && len(res.SelectedProducers) > 0 && err == nil {
			s.lastUsedSpan.Store(res)
		}
	}()

	// Search backwards from the highest known span ID to find the latest span containing the block
	// Since we iterate from high to low, the first match will be the span with the largest ID among known spans
	for id := int(estimatedSpanId); id >= 0; id-- {
		span, err := s.spanById(ctx, uint64(id))
		if err != nil {
			return nil, err
		}
		if blockNumber >= span.StartBlock && blockNumber <= span.EndBlock {
			// Found a span that contains the block number in known spans
			res = span
			break
		}
		// Check if block number given is out of bounds (future block) for the latest known span
		if id == int(estimatedSpanId) && blockNumber > span.EndBlock {
			// Block is in the future, search future spans
			return s.getFutureSpan(ctx, uint64(id)+1, blockNumber, estimatedSpanId)
		}
	}

	// If we found a candidate in known spans, we still need to check if there are newer spans in future
	// that also contain this block number due to overlapping spans
	if res != nil {
		futureSpan, err := s.getFutureSpan(ctx, estimatedSpanId+1, blockNumber, estimatedSpanId)
		if err == nil && futureSpan != nil {
			// Found a future span that also contains the block, return the newer one
			return futureSpan, nil
		}
		// No future span found or error occurred, return the candidate from known spans
		return res, nil
	}

	return nil, fmt.Errorf("span not found for block %d", blockNumber)
}

// getFutureSpan fetches span for future block number. It is mostly needed during snap sync.
func (s *SpanStore) getFutureSpan(ctx context.Context, id uint64, blockNumber uint64, latestKnownSpanId uint64) (*borTypes.Span, error) {
	latestSpan, err := s.getLatestSpan(ctx)
	if err != nil || latestSpan == nil {
		return nil, err
	}

	var candidateSpan *borTypes.Span
	skippedSpans := 0
	for {
		if id > latestSpan.Id {
			if candidateSpan == nil {
				return nil, fmt.Errorf("span not found for block %d", blockNumber)
			}
			return candidateSpan, nil
		}
		span, err := s.spanById(ctx, id)
		if err != nil {
			if candidateSpan == nil {
				return nil, err
			}
			return candidateSpan, nil
		}
		if blockNumber >= span.StartBlock && blockNumber <= span.EndBlock {
			candidateSpan = span
			skippedSpans = 0
		}
		if blockNumber < span.StartBlock {
			skippedSpans++
			if skippedSpans > 1 {
				if candidateSpan == nil {
					return nil, fmt.Errorf("span not found for block %d", blockNumber)
				}
				return candidateSpan, nil
			}
		}
		id++
	}
}

// estimateSpanId returns the corresponding span id for the given block number in a deterministic way.
func (s *SpanStore) estimateSpanId(blockNumber uint64) uint64 {
	if blockNumber > zerothSpanEnd && blockNumber > 0 {
		if s.lastUsedSpan.Load() != nil {
			lastUsedSpan := s.lastUsedSpan.Load()
			startBlock := lastUsedSpan.StartBlock
			endBlock := lastUsedSpan.EndBlock
			if blockNumber > endBlock {
				return lastUsedSpan.Id + 1 + (blockNumber-endBlock-1)/defaultSpanLength
			} else if blockNumber < startBlock {
				// Calculate how many spans to go back. (startBlock - blockNumber + defaultSpanLength - 1) / defaultSpanLength is ceil((startBlock - blockNumber)/defaultSpanLength)
				spansToDecrement := 1 + (startBlock-blockNumber-1)/defaultSpanLength
				if lastUsedSpan.Id >= spansToDecrement { // Prevent underflow for uint64
					return lastUsedSpan.Id - spansToDecrement
				} else {
					return 1 + (blockNumber-zerothSpanEnd-1)/defaultSpanLength
				}
			} else {
				return lastUsedSpan.Id
			}
		}
		return 1 + (blockNumber-zerothSpanEnd-1)/defaultSpanLength
	}

	return 0
}

// setHeimdallClient sets the underlying heimdall client to be used. It is useful in
// tests where mock heimdall client is set after creation of bor instance explicitly.
func (s *SpanStore) setHeimdallClient(client IHeimdallClient) {
	s.heimdallClient = client
}

// getMockSpan0 constructs a mock span 0 by fetching validator set from genesis state. This should
// only be used in tests where heimdall client is not available.
func getMockSpan0(ctx context.Context, spanner Spanner, chainId string) (*borTypes.Span, error) {
	if spanner == nil {
		return nil, fmt.Errorf("spanner not available to fetch validator set")
	}

	// Fetch validators from genesis state
	vals, err := spanner.GetCurrentValidatorsByBlockNrOrHash(ctx, rpc.BlockNumberOrHashWithNumber(0), 0)
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("no validators found for genesis, cannot create mock span 0")
	}
	validatorSet := valset.ValidatorSet{
		Validators: vals,
		Proposer:   vals[0],
	}

	return &borTypes.Span{
		Id:                0,
		StartBlock:        0,
		EndBlock:          255,
		ValidatorSet:      span.ConvertBorValSetToHeimdallValSet(&validatorSet),
		SelectedProducers: span.ConvertBorValidatorsToHeimdallValidators(vals),
		BorChainId:        chainId,
	}, nil
}

// Close cancels the background routine and cleans up resources
func (s *SpanStore) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Wait for a new span whose selected producers are different from the current header author
func (s *SpanStore) waitForNewSpan(targetBlockNumber uint64, currentHeaderAuthor common.Address, timeout time.Duration) (bool, error) {
	delay := 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	currentSpan, err := s.spanByBlockNumber(ctx, targetBlockNumber)
	if err != nil {
		return false, err
	}

	for {
		if currentSpan.StartBlock <= targetBlockNumber && currentSpan.EndBlock >= targetBlockNumber {
			if len(currentSpan.SelectedProducers) > 0 && common.HexToAddress(currentSpan.SelectedProducers[0].Signer) != currentHeaderAuthor {
				return true, nil
			}
		}

		select {
		case <-ctx.Done():
			return false, nil
		case <-time.After(delay):
			// Only update span after delay if we need to keep waiting
			currentSpan, err = s.spanByBlockNumber(ctx, targetBlockNumber)
			if err != nil {
				return false, err
			}
		}
	}
}
