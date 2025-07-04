package bor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	hmm "github.com/ethereum/go-ethereum/heimdall-migration-monitor"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	lru "github.com/hashicorp/golang-lru"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
)

// maxSpanFetchLimit denotes maximum number of future spans to fetch. During snap sync,
// we verify very large batch of headers. The maximum range is not known as of now and
// hence we set a very high limit. It can be reduced later.
const maxSpanFetchLimit = 10_000

// SpanStore acts as a simple middleware to cache span data populated from heimdall. It is used
// in multiple places of bor consensus for verification.
type SpanStore struct {
	store *lru.ARCCache

	heimdallClient IHeimdallClient
	spanner        Spanner

	latestKnownSpanId uint64
	chainId           string

	db ethdb.Database
}

func NewSpanStore(heimdallClient IHeimdallClient, spanner Spanner, chainId string, db ethdb.Database) SpanStore {
	cache, _ := lru.NewARC(10)
	return SpanStore{
		store:             cache,
		heimdallClient:    heimdallClient,
		spanner:           spanner,
		latestKnownSpanId: 0,
		chainId:           chainId,
		db:                db,
	}
}

// spanById returns a span given its id. It fetches span from heimdall if not found in cache.
func (s *SpanStore) spanById(ctx context.Context, spanId uint64) (*span.HeimdallSpan, error) {
	var currentSpan *span.HeimdallSpan
	if value, ok := s.store.Get(spanId); ok {
		currentSpan, _ = value.(*span.HeimdallSpan)
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
		getSpanLength := func(startBlock, endBlock uint64) uint64 {
			return (endBlock - startBlock) + 1
		}
		getSpanStartBlock := func(latestSpanId, latestSpanStartBlock, spanLength uint64) uint64 {
			return latestSpanStartBlock + ((spanId - latestSpanId) * spanLength)
		}
		getSpanEndBlock := func(startBlock uint64, spanLength uint64) uint64 {
			return startBlock + spanLength - 1
		}
		retryWithBackoff := func(exec func() bool) {
			i := 1
			for {
				if done := exec(); done {
					return
				}

				time.Sleep(time.Duration(i) * time.Second)

				if i < 5 {
					i++
				}
			}
		}

		retryWithBackoff(func() bool {
			if hmm.IsHeimdallV2 {
				var response *borTypes.Span

				var err error
				response, err = s.heimdallClient.GetSpanV2(ctx, spanId)
				if err == nil {
					currentSpan = span.ConvertV2SpanToV1Span(response)
					return true
				}
				log.Error("Error while fetching heimdallv2 span", "error", err)
				response = s.getLatestHeimdallSpanV2()
				if response != nil {
					if spanId < response.Id {
						log.Error("Span id is less than latest heimdallv2 span id, using latest span", "spanId", spanId, "latestSpanId", response.Id)
						return true
					}
					originalSpanID := response.Id
					response.Id = spanId
					spanLength := getSpanLength(response.StartBlock, response.EndBlock)
					response.StartBlock = getSpanStartBlock(originalSpanID, response.StartBlock, spanLength)
					response.EndBlock = getSpanEndBlock(response.StartBlock, spanLength)

					if err := s.setStartBlockHeimdallSpanID(response.StartBlock, originalSpanID); err != nil {
						log.Error("Error while saving heimdallv2 span id to db", "error", err)
						return false
					}

					currentSpan = span.ConvertV2SpanToV1Span(response)

					return true
				}

				return false
			}

			var response *span.HeimdallSpan

			var err error
			response, err = s.heimdallClient.GetSpanV1(ctx, spanId)
			if err == nil {
				currentSpan = response
				return true
			}
			log.Error("Error while fetching heimdallv1 span", "error", err)
			response = s.getLatestHeimdallSpanV1()
			if response != nil {
				originalSpanID := response.Id
				response.Id = spanId
				spanLength := getSpanLength(response.StartBlock, response.EndBlock)
				response.StartBlock = getSpanStartBlock(originalSpanID, response.StartBlock, spanLength)
				response.EndBlock = getSpanEndBlock(response.StartBlock, spanLength)

				if err := s.setStartBlockHeimdallSpanID(response.StartBlock, originalSpanID); err != nil {
					log.Error("Error while saving heimdallv1 span id to db", "error", err)
					return false
				}

				currentSpan = response

				return true
			}

			return false
		})
	}

	if currentSpan == nil {
		return nil, fmt.Errorf("span not found for id %d", spanId)
	}

	s.store.Add(spanId, currentSpan)
	if currentSpan.Span.Id > s.latestKnownSpanId {
		s.latestKnownSpanId = currentSpan.Id
	}

	return currentSpan, nil
}

func (s *SpanStore) setStartBlockHeimdallSpanID(startBlock, spanID uint64) error {
	spanIDBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(spanIDBytes, spanID)

	startBlockBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(startBlockBytes, startBlock)

	if err := s.db.Put(append(rawdb.SpanStartBlockToHeimdallSpanIDKey, startBlockBytes...), spanIDBytes); err != nil {
		return err
	}

	return nil
}

func (s *SpanStore) getLatestHeimdallSpanV1() *span.HeimdallSpan {
	storedSpanBytes, err := s.db.Get(rawdb.LastHeimdallV1SpanKey)
	if err != nil {
		log.Error("Error while fetching heimdallv1 span from db", "error", err)
		return nil
	}

	if len(storedSpanBytes) == 0 {
		log.Info("No heimdallv1 span found in db")
		return nil
	}

	var storedSpan span.HeimdallSpan
	if err := json.Unmarshal(storedSpanBytes, &storedSpan); err != nil {
		log.Error("Error while unmarshalling heimdallv1 span", "error", err)
		return nil
	}

	return &storedSpan
}

func (s *SpanStore) getLatestHeimdallSpanV2() *borTypes.Span {
	storedSpanBytes, err := s.db.Get(rawdb.LastHeimdallV2SpanKey)
	if err != nil {
		log.Error("Error while fetching heimdallv2 span from db", "error", err)
		return nil
	}

	if len(storedSpanBytes) == 0 {
		log.Info("No heimdallv2 span found in db")
		return nil
	}

	var storedSpan borTypes.Span
	if err := json.Unmarshal(storedSpanBytes, &storedSpan); err != nil {
		log.Error("Error while unmarshalling heimdallv2 span", "error", err)
		return nil
	}

	return &storedSpan
}

// spanByBlockNumber returns a span given a block number. It fetches span from heimdall if not found in cache. It
// assumes that a span has been committed before (i.e. is current or past span) and returns an error if
// asked for a future span. This is safe to assume as we don't have a way to find out span id for a future block
// unless we hardcode the span length (which we don't want to).
func (s *SpanStore) spanByBlockNumber(ctx context.Context, blockNumber uint64) (*span.HeimdallSpan, error) {
	// As we don't persist latest known span to db, we loose the value on restarts. This leads to multiple heimdall calls
	// which can be avoided. Hence we estimate the span id from block number which updates the latest known span id. Note
	// that we still check if the block number lies in the range of span before returning it.
	estimatedSpanId := estimateSpanId(blockNumber)
	// Ignore the return value of this span as we validate it later in the loop
	_, err := s.spanById(ctx, estimatedSpanId)
	if err != nil {
		return nil, err
	}
	// Iterate over all spans and check for number. This is to replicate the behaviour implemented in
	// https://github.com/maticnetwork/genesis-contracts/blob/master/contracts/BorValidatorSet.template#L118-L134
	// This logic is independent of the span length (bit extra effort but maintains equivalence) and will work
	// for all span lengths (even if we change it in future).
	latestKnownSpanId := s.latestKnownSpanId
	for id := int(latestKnownSpanId); id >= 0; id-- {
		span, err := s.spanById(ctx, uint64(id))
		if err != nil {
			return nil, err
		}
		if blockNumber >= span.StartBlock && blockNumber <= span.EndBlock {
			return span, nil
		}
		// Check if block number given is out of bounds
		if id == int(latestKnownSpanId) && blockNumber > span.EndBlock {
			return getFutureSpan(ctx, uint64(id)+1, blockNumber, latestKnownSpanId, s)
		}
	}

	return nil, fmt.Errorf("span not found for block %d", blockNumber)
}

// getFutureSpan fetches span for future block number. It is mostly needed during snap sync.
func getFutureSpan(ctx context.Context, id uint64, blockNumber uint64, latestKnownSpanId uint64, s *SpanStore) (*span.HeimdallSpan, error) {
	for {
		if id > latestKnownSpanId+maxSpanFetchLimit {
			return nil, fmt.Errorf("span not found for block %d", blockNumber)
		}
		span, err := s.spanById(ctx, id)
		if err != nil {
			return nil, err
		}
		if blockNumber >= span.StartBlock && blockNumber <= span.EndBlock {
			return span, nil
		}
		id++
	}
}

// estimateSpanId returns the corresponding span id for the given block number in a deterministic way.
func estimateSpanId(blockNumber uint64) uint64 {
	if blockNumber > zerothSpanEnd {
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
func getMockSpan0(ctx context.Context, spanner Spanner, chainId string) (*span.HeimdallSpan, error) {
	if spanner == nil {
		return nil, fmt.Errorf("spanner not available to fetch validator set")
	}

	// Fetch validators from genesis state
	vals, err := spanner.GetCurrentValidatorsByBlockNrOrHash(ctx, rpc.BlockNumberOrHashWithNumber(0), 0)
	if err != nil {
		return nil, err
	}
	validatorSet := valset.ValidatorSet{
		Validators: vals,
		Proposer:   vals[0],
	}
	selectedProducers := make([]valset.Validator, len(vals))
	for _, v := range vals {
		selectedProducers = append(selectedProducers, *v)
	}
	return &span.HeimdallSpan{
		Span: span.Span{
			Id:         0,
			StartBlock: 0,
			EndBlock:   255,
		},
		ValidatorSet:      validatorSet,
		SelectedProducers: selectedProducers,
		ChainID:           chainId,
	}, nil
}
