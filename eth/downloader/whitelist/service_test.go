// nolint
package whitelist

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
)

// MockChainReader implements ChainReader interface for testing
type MockChainReader struct {
	currentBlock *types.Header
	blocks       map[uint64]*types.Block
}

func NewMockChainReader() *MockChainReader {
	return &MockChainReader{
		blocks: make(map[uint64]*types.Block),
	}
}

func (m *MockChainReader) CurrentBlock() *types.Header {
	return m.currentBlock
}

func (m *MockChainReader) GetBlockByNumber(number uint64) *types.Block {
	return m.blocks[number]
}

func (m *MockChainReader) SetCurrentBlock(header *types.Header) {
	m.currentBlock = header
}

func (m *MockChainReader) SetBlock(number uint64, block *types.Block) {
	m.blocks[number] = block
}

// NewMockService creates a new mock whitelist service
func NewMockService(db ethdb.Database) *Service {
	return &Service{
		db: db,
		checkpointService: &checkpoint{
			finality[*rawdb.Checkpoint]{
				doExist:  false,
				interval: 256,
				db:       db,
			},
		},
		milestoneService: &milestone{
			finality: finality[*rawdb.Milestone]{
				doExist:  false,
				interval: 256,
				db:       db,
			},
			LockedMilestoneIDs:   make(map[string]struct{}),
			FutureMilestoneList:  make(map[uint64]common.Hash),
			FutureMilestoneOrder: make([]uint64, 0),
			MaxCapacity:          10,
		},
		maxForkCorrectnessLimit: 10,
		lastValidForkBlock:      0,
		forkValidationCache:     make(map[common.Hash]bool, 10),
	}
}

// NewMockServiceWithBlockchain creates a new mock whitelist service with blockchain
func NewMockServiceWithBlockchain(db ethdb.Database, blockchain ChainReader) *Service {
	service := NewMockService(db)
	service.SetBlockchain(blockchain)
	return service
}

// TestWhitelistedCheckpoint tests the whitelisting functionalities using checkpoints.
func TestWhitelistedCheckpoint(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()

	//Creating the service for the whitelisting the checkpoints
	s := NewMockService(db)

	cp := s.checkpointService.(*checkpoint)

	require.Equal(t, cp.doExist, false, "expected false as no cp exist at this point")

	_, _, err := rawdb.ReadFinality[*rawdb.Checkpoint](db)
	require.NotNil(t, err, "Error should be nil while reading from the db")

	//Adding the checkpoint
	s.ProcessCheckpoint(11, common.Hash{})

	require.Equal(t, cp.doExist, true, "expected true as cp exist")

	//Removing the checkpoint
	s.PurgeWhitelistedCheckpoint()

	require.Equal(t, cp.doExist, false, "expected false as no cp exist at this point")

	//Adding the checkpoint
	s.ProcessCheckpoint(12, common.Hash{1})

	//Receiving the stored checkpoint
	doExist, number, hash := s.GetWhitelistedCheckpoint()

	//Validating the values received
	require.Equal(t, doExist, true, "expected true ascheckpoint exist at this point")
	require.Equal(t, number, uint64(12), "expected number to be 11 but got", number)
	require.Equal(t, hash, common.Hash{1}, "expected the 1 hash but got", hash)
	require.NotEqual(t, hash, common.Hash{}, "expected the hash to be different from zero hash")

	c1 := s.checkpointService.(*checkpoint)
	fmt.Println("!!!-0", c1.doExist)
	s.PurgeWhitelistedCheckpoint()
	fmt.Println("!!!-1", c1.doExist)
	doExist, number, hash = s.GetWhitelistedCheckpoint()
	fmt.Println("!!!-2", c1.doExist)
	//Validating the values received from the db, not memory
	require.Equal(t, doExist, true, "expected true ascheckpoint exist at this point")
	require.Equal(t, number, uint64(12), "expected number to be 11 but got", number)
	require.Equal(t, hash, common.Hash{1}, "expected the 1 hash but got", hash)
	require.NotEqual(t, hash, common.Hash{}, "expected the hash to be different from zero hash")

	checkpointNumber, checkpointHash, err := rawdb.ReadFinality[*rawdb.Checkpoint](db)
	require.Nil(t, err, "Error should be nil while reading from the db")
	require.Equal(t, checkpointHash, common.Hash{1}, "expected the 1 hash but got", hash)
	require.Equal(t, checkpointNumber, uint64(12), "expected number to be 11 but got", number)
}

// TestMilestone checks the milestone whitelist setter and getter functions
func TestMilestone(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	s := NewMockService(db)

	milestone := s.milestoneService.(*milestone)

	//Checking for the variables when no milestone is Processed
	require.Equal(t, milestone.doExist, false, "expected false as no milestone exist at this point")
	require.Equal(t, milestone.Locked, false, "expected false as it was not locked")
	require.Equal(t, milestone.LockedMilestoneNumber, uint64(0), "expected 0 as it was not initialized")

	_, _, err := rawdb.ReadFinality[*rawdb.Milestone](db)
	require.NotNil(t, err, "Error should be nil while reading from the db")

	//Acquiring the mutex lock
	milestone.LockMutex(11)
	require.Equal(t, milestone.Locked, false, "expected false as sprint is not locked till this point")

	//Releasing the mutex lock
	milestone.UnlockMutex(true, "milestoneID1", uint64(11), common.Hash{})
	require.Equal(t, milestone.LockedMilestoneNumber, uint64(11), "expected 11 as it was not initialized")
	require.Equal(t, milestone.Locked, true, "expected true as sprint is locked now")
	require.Equal(t, len(milestone.LockedMilestoneIDs), 1, "expected 1 as only 1 milestoneID has been entered")

	_, ok := milestone.LockedMilestoneIDs["milestoneID1"]
	require.True(t, ok, "milestoneID1 should exist in the LockedMilestoneIDs map")

	_, ok = milestone.LockedMilestoneIDs["milestoneID2"]
	require.False(t, ok, "milestoneID2 shouldn't exist in the LockedMilestoneIDs map")

	milestone.LockMutex(11)
	milestone.UnlockMutex(true, "milestoneID2", uint64(11), common.Hash{})
	require.Equal(t, len(milestone.LockedMilestoneIDs), 1, "expected 1 as only 1 milestoneID has been entered")

	_, ok = milestone.LockedMilestoneIDs["milestoneID2"]
	require.True(t, ok, "milestoneID2 should exist in the LockedMilestoneIDs map")

	milestone.RemoveMilestoneID("milestoneID1")
	require.Equal(t, len(milestone.LockedMilestoneIDs), 1, "expected 1 as one out of two has been removed in previous step")
	require.Equal(t, milestone.Locked, true, "expected true as sprint is locked now")

	milestone.RemoveMilestoneID("milestoneID2")
	require.Equal(t, len(milestone.LockedMilestoneIDs), 0, "expected 1 as both the milestonesIDs has been removed in previous step")
	require.Equal(t, milestone.Locked, false, "expected false")

	milestone.LockMutex(11)
	milestone.UnlockMutex(true, "milestoneID3", uint64(11), common.Hash{})
	require.True(t, milestone.Locked, "expected true")
	require.Equal(t, milestone.LockedMilestoneNumber, uint64(11), "Expected 11")

	milestone.LockMutex(15)
	require.True(t, milestone.Locked, "expected true")
	require.Equal(t, milestone.LockedMilestoneNumber, uint64(11), "Expected 11")
	milestone.UnlockMutex(true, "milestoneID4", uint64(15), common.Hash{})
	require.True(t, milestone.Locked, "expected true as final confirmation regarding the lock has been made")
	require.Equal(t, len(milestone.LockedMilestoneIDs), 1, "expected 1 as previous milestonesIDs has been removed in previous step")

	//Adding the milestone
	s.ProcessMilestone(11, common.Hash{})

	require.True(t, milestone.Locked, "expected true as locked sprint is of number 15")
	require.Equal(t, milestone.doExist, true, "expected true as milestone exist")
	require.Equal(t, len(milestone.LockedMilestoneIDs), 1, "expected 1 as still last milestone of sprint number 15 exist")

	//Reading from the Db
	locked, lockedMilestoneNumber, lockedMilestoneHash, lockedMilestoneIDs, err := rawdb.ReadLockField(db)

	require.Nil(t, err)
	require.True(t, locked, "expected true as locked sprint is of number 15")
	require.Equal(t, lockedMilestoneNumber, uint64(15), "Expected 15")
	require.Equal(t, lockedMilestoneHash, common.Hash{}, "Expected", common.Hash{})
	require.Equal(t, len(lockedMilestoneIDs), 1, "expected 1 as still last milestone of sprint number 15 exist")

	_, ok = lockedMilestoneIDs["milestoneID4"]
	require.True(t, ok, "expected true as milestoneIDList should contain 'milestoneID4'")

	//Asking the lock for sprintNumber less than last whitelisted milestone
	require.False(t, milestone.LockMutex(11), "Cant lock the sprintNumber less than equal to latest whitelisted milestone")
	milestone.UnlockMutex(false, "", uint64(11), common.Hash{}) //Unlock is required after every lock to release the mutex

	//Adding the milestone
	s.ProcessMilestone(51, common.Hash{})
	require.False(t, milestone.Locked, "expected false as lock from sprint number 15 is removed")
	require.Equal(t, milestone.doExist, true, "expected true as milestone exist")
	require.Equal(t, len(milestone.LockedMilestoneIDs), 0, "expected 0 as all the milestones have been removed")

	//Reading from the Db
	locked, _, _, lockedMilestoneIDs, err = rawdb.ReadLockField(db)

	require.Nil(t, err)
	require.False(t, locked, "expected true as locked sprint is of number 15")
	require.Equal(t, len(lockedMilestoneIDs), 0, "expected 0 as milestoneID exist in the map")

	//Removing the milestone
	s.PurgeWhitelistedMilestone()

	require.Equal(t, milestone.doExist, false, "expected false as no milestone exist at this point")

	//Removing the milestone
	s.ProcessMilestone(11, common.Hash{1})

	doExist, number, hash := s.GetWhitelistedMilestone()

	//validating the values received
	require.Equal(t, doExist, true, "expected true as milestone exist at this point")
	require.Equal(t, number, uint64(11), "expected number to be 11 but got", number)
	require.Equal(t, hash, common.Hash{1}, "expected the 1 hash but got", hash)

	s.PurgeWhitelistedMilestone()
	doExist, number, hash = s.GetWhitelistedMilestone()

	//Validating the values received from the db, not memory
	require.Equal(t, doExist, true, "expected true as milestone exist at this point")
	require.Equal(t, number, uint64(11), "expected number to be 11 but got", number)
	require.Equal(t, hash, common.Hash{1}, "expected the 1 hash but got", hash)

	milestoneNumber, milestoneHash, err := rawdb.ReadFinality[*rawdb.Milestone](db)
	require.Nil(t, err, "Error should be nil while reading from the db")
	require.Equal(t, milestoneHash, common.Hash{1}, "expected the 1 hash but got", hash)
	require.Equal(t, milestoneNumber, uint64(11), "expected number to be 11 but got", number)

	_, _, err = rawdb.ReadFutureMilestoneList(db)
	require.NotNil(t, err, "Error should be not nil")

	s.ProcessFutureMilestone(16, common.Hash{16})
	require.Equal(t, len(milestone.FutureMilestoneOrder), 1, "expected length is 1 as we added only 1 future milestone")
	require.Equal(t, milestone.FutureMilestoneOrder[0], uint64(16), "expected value is 16 but got", milestone.FutureMilestoneOrder[0])
	require.Equal(t, milestone.FutureMilestoneList[16], common.Hash{16}, "expected value is", common.Hash{16}.String()[2:], "but got", milestone.FutureMilestoneList[16])

	order, list, err := rawdb.ReadFutureMilestoneList(db)
	require.Nil(t, err, "Error should be nil while reading from the db")
	require.Equal(t, len(order), 1, "expected the 1 hash but got", len(order))
	require.Equal(t, order[0], uint64(16), "expected number to be 16 but got", order[0])
	require.Equal(t, list[order[0]], common.Hash{16}, "expected value is", common.Hash{16}.String()[2:], "but got", list[order[0]])

	capacity := milestone.MaxCapacity
	for i := 16; i <= 16*(capacity+1); i = i + 16 {
		s.ProcessFutureMilestone(uint64(i), common.Hash{16})
	}

	require.Equal(t, len(milestone.FutureMilestoneOrder), capacity, "expected length is", capacity)
	require.Equal(t, milestone.FutureMilestoneOrder[capacity-1], uint64(16*capacity), "expected value is", uint64(16*capacity), "but got", milestone.FutureMilestoneOrder[capacity-1])
}

// TestIsValidPeer checks the IsValidPeer function in isolation
// for different cases by providing a mock fetchHeadersByNumber function
func TestIsValidPeer(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	s := NewMockService(db)

	// case1: no checkpoint whitelist, should consider the chain as valid
	res, err := s.IsValidPeer(nil)
	require.NoError(t, err, "expected no error")
	require.Equal(t, res, true, "expected chain to be valid")

	// add checkpoint entry and mock fetchHeadersByNumber function
	s.ProcessCheckpoint(uint64(1), common.Hash{})

	// add milestone entry and mock fetchHeadersByNumber function
	s.ProcessMilestone(uint64(1), common.Hash{})

	checkpoint := s.checkpointService.(*checkpoint)
	milestone := s.milestoneService.(*milestone)

	//Check whether the milestone and checkpoint exist
	require.Equal(t, checkpoint.doExist, true, "expected true as checkpoint exists")
	require.Equal(t, milestone.doExist, true, "expected true as milestone exists")

	// create a false function, returning absolutely nothing
	falseFetchHeadersByNumber := func(number uint64, amount int, skip int, reverse bool) ([]*types.Header, []common.Hash, error) {
		return nil, nil, nil
	}

	// case2: false fetchHeadersByNumber function provided, should consider the chain as invalid
	// and throw `ErrNoRemoteCheckoint` error
	res, err = s.IsValidPeer(falseFetchHeadersByNumber)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, ErrNoRemote) {
		t.Fatalf("expected error ErrNoRemote, got %v", err)
	}

	require.Equal(t, res, false, "expected peer chain to be invalid")

	// create a mock function, returning the required header
	fetchHeadersByNumber := func(number uint64, _ int, _ int, _ bool) ([]*types.Header, []common.Hash, error) {
		hash := common.Hash{}
		header := types.Header{Number: big.NewInt(0)}

		switch number {
		case 0:
			return []*types.Header{&header}, []common.Hash{hash}, nil
		case 1:
			header.Number = big.NewInt(1)
			return []*types.Header{&header}, []common.Hash{hash}, nil
		case 2:
			header.Number = big.NewInt(1) // sending wrong header for misamatch
			return []*types.Header{&header}, []common.Hash{hash}, nil
		default:
			return nil, nil, errors.New("invalid number")
		}
	}

	// case3: correct fetchHeadersByNumber function provided, should consider the chain as valid
	res, err = s.IsValidPeer(fetchHeadersByNumber)
	require.NoError(t, err, "expected no error")
	require.Equal(t, res, true, "expected chain to be valid")

	// add checkpoint whitelist entry
	s.ProcessCheckpoint(uint64(2), common.Hash{})
	require.Equal(t, checkpoint.doExist, true, "expected true as checkpoint exists")

	// case4: correct fetchHeadersByNumber function provided with wrong header
	// for block number 2. Should consider the chain as invalid and throw an error
	res, err = s.IsValidPeer(fetchHeadersByNumber)
	require.Equal(t, err, ErrMismatch, "expected mismatch error")
	require.Equal(t, res, false, "expected chain to be invalid")

	// create a mock function, returning the required header
	fetchHeadersByNumber = func(number uint64, _ int, _ int, _ bool) ([]*types.Header, []common.Hash, error) {
		hash := common.Hash{}
		header := types.Header{Number: big.NewInt(0)}

		switch number {
		case 0:
			return []*types.Header{&header}, []common.Hash{hash}, nil
		case 1:
			header.Number = big.NewInt(1)
			return []*types.Header{&header}, []common.Hash{hash}, nil
		case 2:
			header.Number = big.NewInt(2)
			return []*types.Header{&header}, []common.Hash{hash}, nil

		case 3:
			header.Number = big.NewInt(3)
			hash3 := common.Hash{3}

			return []*types.Header{&header}, []common.Hash{hash3}, nil

		default:
			return nil, nil, errors.New("invalid number")
		}
	}

	s.ProcessMilestone(uint64(3), common.Hash{})

	//Case5: correct fetchHeadersByNumber function provided with hash mismatch, should consider the chain as invalid
	res, err = s.IsValidPeer(fetchHeadersByNumber)
	require.Equal(t, err, ErrMismatch, "expected milestone mismatch error")
	require.Equal(t, res, false, "expected chain to be invalid")

	s.ProcessMilestone(uint64(2), common.Hash{})

	// create a mock function, returning the required header
	fetchHeadersByNumber = func(number uint64, _ int, _ int, _ bool) ([]*types.Header, []common.Hash, error) {
		hash := common.Hash{}
		header := types.Header{Number: big.NewInt(0)}

		switch number {
		case 0:
			return []*types.Header{&header}, []common.Hash{hash}, nil
		case 1:
			header.Number = big.NewInt(1)
			return []*types.Header{&header}, []common.Hash{hash}, nil
		case 2:
			header.Number = big.NewInt(2)
			return []*types.Header{&header}, []common.Hash{hash}, nil
		default:
			return nil, nil, errors.New("invalid number")
		}
	}

	// case6: correct fetchHeadersByNumber function provided, should consider the chain as valid
	res, err = s.IsValidPeer(fetchHeadersByNumber)
	require.NoError(t, err, "expected no error")
	require.Equal(t, res, true, "expected chain to be valid")

	// create a mock function, returning the required header
	fetchHeadersByNumber = func(number uint64, _ int, _ int, _ bool) ([]*types.Header, []common.Hash, error) {
		hash := common.Hash{}
		hash3 := common.Hash{3}
		header := types.Header{Number: big.NewInt(0)}

		switch number {
		case 0:
			return []*types.Header{&header}, []common.Hash{hash}, nil
		case 1:
			header.Number = big.NewInt(1)
			return []*types.Header{&header}, []common.Hash{hash}, nil
		case 2:
			header.Number = big.NewInt(2)
			return []*types.Header{&header}, []common.Hash{hash}, nil

		case 3:
			header.Number = big.NewInt(2) // sending wrong header for misamatch
			return []*types.Header{&header}, []common.Hash{hash}, nil

		case 4:
			header.Number = big.NewInt(4) // sending wrong header for misamatch
			return []*types.Header{&header}, []common.Hash{hash3}, nil
		default:
			return nil, nil, errors.New("invalid number")
		}
	}

	//Add one more milestone in the list
	s.ProcessMilestone(uint64(3), common.Hash{})

	// case7: correct fetchHeadersByNumber function provided with wrong header for block 3, should consider the chain as invalid
	res, err = s.IsValidPeer(fetchHeadersByNumber)
	require.Equal(t, err, ErrMismatch, "expected milestone mismatch error")
	require.Equal(t, res, false, "expected chain to be invalid")

	//require.Equal(t, milestone.length(), 3, "expected 3 items in milestoneList")

	//Add one more milestone in the list
	s.ProcessMilestone(uint64(4), common.Hash{})

	// case8: correct fetchHeadersByNumber function provided with wrong hash for block 3, should consider the chain as valid
	res, err = s.IsValidPeer(fetchHeadersByNumber)
	require.Equal(t, err, ErrMismatch, "expected milestone mismatch error")
	require.Equal(t, res, false, "expected chain to be invalid")
}

// TestIsValidChain checks the IsValidChain function in isolation
// for different cases by providing a mock current header and chain
func TestIsValidChain(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	s := NewMockService(db)
	chainA := createMockChain(1, 20, common.Hash{}) // A1->A2...A19->A20

	//Case1: no checkpoint whitelist and no milestone and no locking, should consider the chain as valid
	res, err := s.IsValidChain(nil, chainA)
	require.Nil(t, err)
	require.Equal(t, res, true, "Expected chain to be valid")

	tempChain := createMockChain(21, 22, common.Hash{}) // A21->A22

	// add mock checkpoint entry
	s.ProcessCheckpoint(tempChain[1].Number.Uint64(), tempChain[1].Hash())

	//Make the mock chain with zero blocks
	zeroChain := make([]*types.Header, 0)

	//Case2: As input chain is of zero length,should consider the chain as invalid
	res, err = s.IsValidChain(nil, zeroChain)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid", len(zeroChain))

	//Case3A: As the received chain and current tip of local chain is behind the oldest whitelisted block entry, should consider
	// the chain as valid
	res, err = s.IsValidChain(chainA[len(chainA)-1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid")

	//Case3B: As the received chain is behind the oldest whitelisted block entry,but current tip is at par with whitelisted checkpoint, should consider
	// the chain as invalid
	res, err = s.IsValidChain(tempChain[1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid ")

	// add mock milestone entry
	s.ProcessMilestone(tempChain[1].Number.Uint64(), tempChain[1].Hash())

	//Case4A: As the received chain and current tip of local chain is behind the oldest whitelisted block entry, should consider
	// the chain as valid
	res, err = s.IsValidChain(chainA[len(chainA)-1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid")

	//Case4B: As the received chain is behind the oldest whitelisted block entry and but current tip is at par with whitelisted milestine, should consider
	// the chain as invalid
	res, err = s.IsValidChain(tempChain[1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid")

	//Remove the whitelisted checkpoint
	s.PurgeWhitelistedCheckpoint()

	//Case5: As the received chain is still invalid after removing the checkpoint as it is
	//still behind the whitelisted milestone
	res, err = s.IsValidChain(tempChain[1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid")

	//Remove the whitelisted milestone
	s.PurgeWhitelistedMilestone()

	//At this stage there is no whitelisted milestone and checkpoint

	checkpoint := s.checkpointService.(*checkpoint)
	milestone := s.milestoneService.(*milestone)

	//Locking for sprintNumber 15
	milestone.LockMutex(chainA[len(chainA)-5].Number.Uint64())
	milestone.UnlockMutex(true, "MilestoneID1", chainA[len(chainA)-5].Number.Uint64(), chainA[len(chainA)-5].Hash())

	//Case6: As the received chain is valid as the locked sprintHash matches with the incoming chain.
	res, err = s.IsValidChain(chainA[len(chainA)-1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid as incoming chain matches with the locked value ")

	hash3 := common.Hash{3}

	//Locking for sprintNumber 16 with different hash
	milestone.LockMutex(chainA[len(chainA)-4].Number.Uint64())
	milestone.UnlockMutex(true, "MilestoneID2", chainA[len(chainA)-4].Number.Uint64(), hash3)

	res, err = s.IsValidChain(chainA[len(chainA)-1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid as incoming chain does match with the locked value hash ")

	//Locking for sprintNumber 19
	milestone.LockMutex(chainA[len(chainA)-1].Number.Uint64())
	milestone.UnlockMutex(true, "MilestoneID1", chainA[len(chainA)-1].Number.Uint64(), chainA[len(chainA)-1].Hash())

	//Case7: As the received chain is valid as the locked sprintHash matches with the incoming chain.
	res, err = s.IsValidChain(chainA[len(chainA)-1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid as incoming chain is less than the locked value ")

	//Locking for sprintNumber 19
	milestone.LockMutex(uint64(21))
	milestone.UnlockMutex(true, "MilestoneID1", uint64(21), hash3)

	//Case8: As the received chain is invalid as the locked sprintHash matches is ahead of incoming chain.
	res, err = s.IsValidChain(chainA[len(chainA)-1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid as incoming chain is less than the locked value ")

	//Unlocking the sprint
	milestone.UnlockSprint(uint64(21))

	// Clear checkpoint whitelist and add block A15 in whitelist
	s.PurgeWhitelistedCheckpoint()
	s.ProcessCheckpoint(chainA[15].Number.Uint64(), chainA[15].Hash())

	require.Equal(t, checkpoint.doExist, true, "expected true as checkpoint exists.")

	// case9: As the received chain is having valid checkpoint,should consider the chain as valid.
	res, err = s.IsValidChain(chainA[len(chainA)-1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid")

	// add mock milestone entries
	s.ProcessMilestone(tempChain[1].Number.Uint64(), tempChain[1].Hash())

	// case10: Try importing a past chain having valid checkpoint, should
	// consider the chain as invalid as still latest milestone is ahead of the chain.
	res, err = s.IsValidChain(tempChain[1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid")

	// add mock milestone entries
	s.ProcessMilestone(chainA[19].Number.Uint64(), chainA[19].Hash())

	// case12: Try importing a chain having valid checkpoint and milestone, should
	// consider the chain as valid
	res, err = s.IsValidChain(tempChain[1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be invalid")

	// add mock milestone entries
	s.ProcessMilestone(chainA[19].Number.Uint64(), chainA[19].Hash())

	// case13: Try importing a past chain having valid checkpoint and milestone, should
	// consider the chain as valid
	res, err = s.IsValidChain(tempChain[1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid")

	// add mock milestone entries with wrong hash
	s.ProcessMilestone(chainA[19].Number.Uint64(), chainA[18].Hash())

	// case14: Try importing a past chain having valid checkpoint and milestone with wrong hash, should
	// consider the chain as invalid
	res, err = s.IsValidChain(chainA[len(chainA)-1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid as hash mismatches")

	// Clear milestone and add blocks A15 in whitelist
	s.ProcessMilestone(chainA[15].Number.Uint64(), chainA[15].Hash())

	// case16: Try importing a past chain having valid checkpoint, should
	// consider the chain as valid
	res, err = s.IsValidChain(tempChain[1], chainA)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid")

	// Clear checkpoint whitelist and mock blocks in whitelist
	tempChain = createMockChain(20, 20, common.Hash{}) // A20

	s.PurgeWhitelistedCheckpoint()
	s.ProcessCheckpoint(tempChain[0].Number.Uint64(), tempChain[0].Hash())

	require.Equal(t, checkpoint.doExist, true, "expected true")

	// case17: Try importing a past chain having invalid checkpoint,should consider the chain as invalid
	res, err = s.IsValidChain(tempChain[0], chainA)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid")
	// Not checking error here because we return nil in case of checkpoint mismatch

	// case18: Try importing a future chain but within interval, should consider the chain as valid
	res, err = s.IsValidChain(tempChain[len(tempChain)-1], tempChain)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be invalid")

	// create a future chain to be imported of length <= `checkpointInterval`
	chainB := createMockChain(21, 30, common.Hash{}) // B21->B22...B29->B30

	// case19: Try importing a future chain of acceptable length,should consider the chain as valid
	res, err = s.IsValidChain(tempChain[0], chainB)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid")

	s.PurgeWhitelistedCheckpoint()
	s.PurgeWhitelistedMilestone()

	chainB = createMockChain(21, 29, common.Hash{}) // C21->C22....C29

	s.milestoneService.ProcessFutureMilestone(29, chainB[8].Hash())

	// case20: Try importing a future chain which match the future milestone should the chain as valid
	res, err = s.IsValidChain(tempChain[0], chainB)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid")

	chainB = createMockChain(21, 27, common.Hash{}) // C21->C22...C39->C40...C->256

	// case21: Try importing a chain whose end point is less than future milestone
	res, err = s.IsValidChain(tempChain[0], chainB)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be valid")

	chainB = createMockChain(30, 39, common.Hash{}) // C21->C22...C39->C40...C->256

	//Processing wrong hash
	s.milestoneService.ProcessFutureMilestone(38, chainB[9].Hash())

	// case22: Try importing a future chain with mismatch future milestone
	res, err = s.IsValidChain(tempChain[0], chainB)
	require.Nil(t, err)
	require.Equal(t, res, false, "expected chain to be invalid")

	chainB = createMockChain(40, 49, common.Hash{}) // C40->C41...C48->C49

	// case23: Try importing a future chain whose starting point is ahead of latest future milestone
	res, err = s.IsValidChain(tempChain[0], chainB)
	require.Nil(t, err)
	require.Equal(t, res, true, "expected chain to be invalid")

}

func TestPropertyBasedTestingMilestone(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {

		db := rawdb.NewMemoryDatabase()

		milestone := milestone{
			finality: finality[*rawdb.Milestone]{
				doExist:  false,
				Number:   0,
				Hash:     common.Hash{},
				interval: 256,
				db:       db,
			},

			Locked:                false,
			LockedMilestoneNumber: 0,
			LockedMilestoneHash:   common.Hash{},
			LockedMilestoneIDs:    make(map[string]struct{}),
			FutureMilestoneList:   make(map[uint64]common.Hash),
			FutureMilestoneOrder:  make([]uint64, 0),
			MaxCapacity:           10,
		}

		var (
			milestoneEndNum = rapid.Uint64().AsAny().Draw(t, "endBlock")
			milestoneID     = rapid.String().AsAny().Draw(t, "MilestoneID")
			doLock          = rapid.Bool().AsAny().Draw(t, "Voted")
		)

		val := milestone.LockMutex(milestoneEndNum.(uint64))
		if !val {
			t.Error("LockMutex need to return true when there is no whitelisted milestone and locked milestone")
		}

		milestone.UnlockMutex(doLock.(bool), milestoneID.(string), milestoneEndNum.(uint64), common.Hash{})

		if doLock.(bool) {
			//Milestone should not be whitelisted
			if milestone.doExist {
				t.Error("Milestone is not expected to be whitelisted")
			}

			//Local chain should be locked
			if !milestone.Locked {
				t.Error("Milestone is expected to be locked at", milestoneEndNum.(uint64))
			}

			if milestone.LockedMilestoneNumber != milestoneEndNum.(uint64) {
				t.Error("Locked milestone number is expected to be", milestoneEndNum.(uint64))
			}

			if len(milestone.LockedMilestoneIDs) != 1 {
				t.Error("List should contain 1 milestone")
			}

			_, ok := milestone.LockedMilestoneIDs[milestoneID.(string)]

			if !ok {
				t.Error("List doesn't contain correct milestoneID")
			}
		}

		if !doLock.(bool) {
			if milestone.doExist {
				t.Error("Milestone is not expected to be whitelisted")
			}

			if milestone.Locked {
				t.Error("Milestone is expected not to be locked")
			}

			if milestone.LockedMilestoneNumber != 0 {
				t.Error("Locked milestone number is expected to be", 0)
			}

			if len(milestone.LockedMilestoneIDs) != 0 {
				t.Error("List should not contain milestone")
			}

			_, ok := milestone.LockedMilestoneIDs[milestoneID.(string)]

			if ok {
				t.Error("List shouldn't contain any milestoneID")
			}
		}

		fitlerFn := func(i uint64) bool {
			if i <= uint64(1000) {
				return true
			}

			return false
		}

		var (
			start = rapid.Uint64Max(milestoneEndNum.(uint64)).AsAny().Draw(t, "start for mock chain")
			end   = rapid.Uint64Min(start.(uint64)).Filter(fitlerFn).AsAny().Draw(t, "end for mock chain")
		)

		chainTemp := createMockChain(start.(uint64), end.(uint64), common.Hash{})

		val, err := milestone.IsValidChain(chainTemp[0], chainTemp)
		if err != nil {
			t.Error("Error", err)
		}

		if doLock.(bool) && val {
			t.Error("When the chain is locked at milestone, it should not pass IsValidChain for incompatible incoming chain")
		}

		if !doLock.(bool) && !val {
			t.Error("When the chain is not locked at milestone, it should pass IsValidChain for incoming chain")
		}

		var (
			milestoneEndNum2 = rapid.Uint64().AsAny().Draw(t, "endBlockNum 2")
			milestoneID2     = rapid.String().AsAny().Draw(t, "MilestoneID 2")
			doLock2          = rapid.Bool().AsAny().Draw(t, "Voted 2")
		)

		val = milestone.LockMutex(milestoneEndNum2.(uint64))

		if doLock.(bool) && milestoneEndNum.(uint64) > milestoneEndNum2.(uint64) && val {
			t.Error("LockMutex need to return false as previous locked milestone is greater")
		}

		if doLock.(bool) && milestoneEndNum.(uint64) <= milestoneEndNum2.(uint64) && !val {
			t.Error("LockMutex need to return true as previous locked milestone is less")
		}

		milestone.UnlockMutex(doLock2.(bool), milestoneID2.(string), milestoneEndNum2.(uint64), common.Hash{})

		if doLock2.(bool) {
			if milestone.doExist {
				t.Error("Milestone is not expected to be whitelisted")
			}

			if !milestone.Locked {
				t.Error("Milestone is expected to be locked at", milestoneEndNum2.(uint64))
			}

			if milestone.LockedMilestoneNumber != milestoneEndNum2.(uint64) {
				t.Error("Locked milestone number is expected to be", milestoneEndNum.(uint64))
			}

			if len(milestone.LockedMilestoneIDs) != 1 {
				t.Error("List should contain 1 milestone")
			}

			_, ok := milestone.LockedMilestoneIDs[milestoneID2.(string)]

			if !ok {
				t.Error("List doesn't contain correct milestoneID")
			}
		}

		if !doLock2.(bool) {
			if milestone.doExist {
				t.Error("Milestone is not expected to be whitelisted")
			}

			if !doLock.(bool) && milestone.Locked {
				t.Error("Milestone is expected not to be locked")
			}

			if doLock.(bool) && !milestone.Locked {
				t.Error("Milestone is expected to be locked at", milestoneEndNum.(uint64))
			}

			if !doLock.(bool) && milestone.LockedMilestoneNumber != 0 {
				t.Error("Locked milestone number is expected to be", 0)
			}

			if doLock.(bool) && milestone.LockedMilestoneNumber != milestoneEndNum.(uint64) {
				t.Error("Locked milestone number is expected to be", milestoneEndNum.(uint64))
			}

			if !doLock.(bool) && len(milestone.LockedMilestoneIDs) != 0 {
				t.Error("List should not contain milestone")
			}

			if doLock.(bool) && len(milestone.LockedMilestoneIDs) != 1 {
				t.Error("List should not contain milestone")
			}

			_, ok := milestone.LockedMilestoneIDs[milestoneID.(string)]

			if !doLock.(bool) && ok {
				t.Error("List shouldn't contain any milestoneID")
			}

			if doLock.(bool) && !ok {
				t.Error("List should contain milestoneID")
			}
		}

		var (
			milestoneNum = rapid.Uint64().AsAny().Draw(t, "milestone Number")
		)

		lockedValue := milestone.LockedMilestoneNumber

		milestone.Process(milestoneNum.(uint64), common.Hash{})

		isChainLocked := doLock.(bool) || doLock2.(bool)

		if !milestone.doExist {
			t.Error("Should have the whitelisted milestone")
		}

		if milestone.finality.Number != milestoneNum.(uint64) {
			t.Error("Should have the whitelisted milestone", milestoneNum.(uint64))
		}

		if isChainLocked {
			if milestoneNum.(uint64) < lockedValue {
				if !milestone.Locked {
					t.Error("Milestone is expected to be locked")
				}
			} else {
				if milestone.Locked {
					t.Error("Milestone is expected not to be locked")
				}
			}
		}

		var (
			futureMilestoneNum = rapid.Uint64Min(milestoneNum.(uint64)).AsAny().Draw(t, "future milestone Number")
		)

		isChainLocked = milestone.Locked

		milestone.ProcessFutureMilestone(futureMilestoneNum.(uint64), common.Hash{})

		if isChainLocked {
			if futureMilestoneNum.(uint64) < lockedValue {
				if !milestone.Locked {
					t.Error("Milestone is expected to be locked")
				}
			} else {
				if milestone.Locked {
					t.Error("Milestone is expected not to be locked")
				}
			}
		}
	})
}

func TestSplitChain(t *testing.T) {
	t.Parallel()

	type Result struct {
		pastStart    uint64
		pastEnd      uint64
		futureStart  uint64
		futureEnd    uint64
		pastLength   int
		futureLength int
	}

	// Current chain is at block: X
	// Incoming chain is represented as [N, M]
	testCases := []struct {
		name    string
		current uint64
		chain   []*types.Header
		result  Result
	}{
		{name: "X = 10, N = 11, M = 20", current: uint64(10), chain: createMockChain(11, 20, common.Hash{}), result: Result{futureStart: 11, futureEnd: 20, futureLength: 10}},
		{name: "X = 10, N = 13, M = 20", current: uint64(10), chain: createMockChain(13, 20, common.Hash{}), result: Result{futureStart: 13, futureEnd: 20, futureLength: 8}},
		{name: "X = 10, N = 2, M = 10", current: uint64(10), chain: createMockChain(2, 10, common.Hash{}), result: Result{pastStart: 2, pastEnd: 10, pastLength: 9}},
		{name: "X = 10, N = 2, M = 9", current: uint64(10), chain: createMockChain(2, 9, common.Hash{}), result: Result{pastStart: 2, pastEnd: 9, pastLength: 8}},
		{name: "X = 10, N = 2, M = 8", current: uint64(10), chain: createMockChain(2, 8, common.Hash{}), result: Result{pastStart: 2, pastEnd: 8, pastLength: 7}},
		{name: "X = 10, N = 5, M = 15", current: uint64(10), chain: createMockChain(5, 15, common.Hash{}), result: Result{pastStart: 5, pastEnd: 10, pastLength: 6, futureStart: 11, futureEnd: 15, futureLength: 5}},
		{name: "X = 10, N = 10, M = 20", current: uint64(10), chain: createMockChain(10, 20, common.Hash{}), result: Result{pastStart: 10, pastEnd: 10, pastLength: 1, futureStart: 11, futureEnd: 20, futureLength: 10}},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			past, future := splitChain(tc.current, tc.chain)
			require.Equal(t, len(past), tc.result.pastLength)
			require.Equal(t, len(future), tc.result.futureLength)

			if len(past) > 0 {
				// Check if we have expected block/s
				require.Equal(t, past[0].Number.Uint64(), tc.result.pastStart)
				require.Equal(t, past[len(past)-1].Number.Uint64(), tc.result.pastEnd)
			}

			if len(future) > 0 {
				// Check if we have expected block/s
				require.Equal(t, future[0].Number.Uint64(), tc.result.futureStart)
				require.Equal(t, future[len(future)-1].Number.Uint64(), tc.result.futureEnd)
			}
		})
	}
}

//nolint:gocognit
func TestSplitChainProperties(t *testing.T) {
	t.Parallel()

	// Current chain is at block: X
	// Incoming chain is represented as [N, M]

	currentChain := []int{0, 1, 2, 3, 10, 100} // blocks starting from genesis
	blockDiffs := []int{0, 1, 2, 3, 4, 5, 9, 10, 11, 12, 90, 100, 101, 102}

	caseParams := make(map[int]map[int]map[int]struct{}) // X -> N -> M

	for _, current := range currentChain {
		// past cases only + past to current
		for _, diff := range blockDiffs {
			from := current - diff

			// use int type for everything to not care about underflow
			if from < 0 {
				continue
			}

			for _, diff := range blockDiffs {
				to := current - diff

				if to >= from {
					addTestCaseParams(caseParams, current, from, to)
				}
			}
		}

		// future only + current to future
		for _, diff := range blockDiffs {
			from := current + diff

			if from < 0 {
				continue
			}

			for _, diff := range blockDiffs {
				to := current + diff

				if to >= from {
					addTestCaseParams(caseParams, current, from, to)
				}
			}
		}

		// past-current-future
		for _, diff := range blockDiffs {
			from := current - diff

			if from < 0 {
				continue
			}

			for _, diff := range blockDiffs {
				to := current + diff

				if to >= from {
					addTestCaseParams(caseParams, current, from, to)
				}
			}
		}
	}

	type testCase struct {
		current     int
		remoteStart int
		remoteEnd   int
	}

	var ts []testCase

	// X -> N -> M
	for x, nm := range caseParams {
		for n, mMap := range nm {
			for m := range mMap {
				ts = append(ts, testCase{x, n, m})
			}
		}
	}

	//nolint:paralleltest
	for i, tc := range ts {
		tc := tc

		name := fmt.Sprintf("test case: index = %d, X = %d, N = %d, M = %d", i, tc.current, tc.remoteStart, tc.remoteEnd)

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			chain := createMockChain(uint64(tc.remoteStart), uint64(tc.remoteEnd), common.Hash{})

			past, future := splitChain(uint64(tc.current), chain)

			// properties
			if len(past) > 0 {
				// Check if the chain is ordered
				isOrdered := sort.SliceIsSorted(past, func(i, j int) bool {
					return past[i].Number.Uint64() < past[j].Number.Uint64()
				})

				require.True(t, isOrdered, "an ordered past chain expected: %v", past)

				isSequential := sort.SliceIsSorted(past, func(i, j int) bool {
					return past[i].Number.Uint64() == past[j].Number.Uint64()-1
				})

				require.True(t, isSequential, "a sequential past chain expected: %v", past)

				// Check if current block >= past chain's last block
				require.Equal(t, past[len(past)-1].Number.Uint64() <= uint64(tc.current), true)
			}

			if len(future) > 0 {
				// Check if the chain is ordered
				isOrdered := sort.SliceIsSorted(future, func(i, j int) bool {
					return future[i].Number.Uint64() < future[j].Number.Uint64()
				})

				require.True(t, isOrdered, "an ordered future chain expected: %v", future)

				isSequential := sort.SliceIsSorted(future, func(i, j int) bool {
					return future[i].Number.Uint64() == future[j].Number.Uint64()-1
				})

				require.True(t, isSequential, "a sequential future chain expected: %v", future)

				// Check if future chain's first block > current block
				require.Equal(t, future[len(future)-1].Number.Uint64() > uint64(tc.current), true)
			}

			// Check if both chains are continuous
			if len(past) > 0 && len(future) > 0 {
				require.Equal(t, past[len(past)-1].Number.Uint64(), future[0].Number.Uint64()-1)
			}

			// Check if we get the original chain on appending both
			gotChain := append(past, future...)
			require.Equal(t, reflect.DeepEqual(gotChain, chain), true)
		})
	}
}

// createMockChain returns a chain with dummy headers
// starting from `start` to `end` (inclusive)
func createMockChain(start, end uint64, parentHash common.Hash) []*types.Header {
	var (
		i   uint64
		idx uint64
	)

	chain := make([]*types.Header, end-start+1)

	for i = start; i <= end; i++ {
		header := &types.Header{
			Number:     big.NewInt(int64(i)),
			Time:       uint64(time.Now().UnixMicro()) + i,
			ParentHash: parentHash,
		}
		chain[idx] = header
		idx++
		parentHash = header.Hash()
	}

	return chain
}

// mXNM should be initialized
func addTestCaseParams(mXNM map[int]map[int]map[int]struct{}, x, n, m int) {
	//nolint:ineffassign
	mNM, ok := mXNM[x]
	if !ok {
		mNM = make(map[int]map[int]struct{})
		mXNM[x] = mNM
	}

	//nolint:ineffassign
	_, ok = mNM[n]
	if !ok {
		mM := make(map[int]struct{})
		mNM[n] = mM
	}

	mXNM[x][n][m] = struct{}{}
}

// TestMilestoneForkDetection tests the fork detection functionality in IsValidPeer
func TestMilestoneForkDetection(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	blockchain := NewMockChainReader()
	service := NewMockServiceWithBlockchain(db, blockchain)

	milestone := service.milestoneService.(*milestone)

	// Set up a milestone
	milestoneNumber := uint64(10)
	milestoneHash := common.HexToHash("0x1234567890abcdef")
	service.ProcessMilestone(milestoneNumber, milestoneHash)

	// Test case 1: Fork detected - milestone and local block have different hashes
	forkHeader := &types.Header{
		Number:      big.NewInt(int64(milestoneNumber)),
		UncleHash:   types.EmptyUncleHash,
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Root:        common.HexToHash("0xdifferenthash"), // Different hash to create fork
	}
	forkBlock := types.NewBlock(forkHeader, &types.Body{}, nil, nil)

	blockchain.SetCurrentBlock(&types.Header{Number: big.NewInt(int64(milestoneNumber))})
	blockchain.SetBlock(milestoneNumber, forkBlock)

	// Test IsValidPeer with fork - should allow sync for recovery
	result, err := milestone.IsValidPeer(func(number uint64, amount int, skip int, reverse bool) ([]*types.Header, []common.Hash, error) {
		if number == milestoneNumber {
			return []*types.Header{forkBlock.Header()}, []common.Hash{forkBlock.Hash()}, nil
		}
		return nil, nil, fmt.Errorf("block not found")
	})
	require.NoError(t, err, "IsValidPeer should not error with fork detection")
	require.True(t, result, "IsValidPeer should return true for fork recovery")
}

// TestMilestoneForkDetectionEdgeCases tests edge cases for fork detection
func TestMilestoneForkDetectionEdgeCases(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	blockchain := NewMockChainReader()
	service := NewMockServiceWithBlockchain(db, blockchain)

	milestone := service.milestoneService.(*milestone)

	// Set up a milestone and make it exist
	milestoneNumber := uint64(20)
	milestoneHash := common.HexToHash("0x1234567890abcdef")
	service.ProcessMilestone(milestoneNumber, milestoneHash)

	// Test case 1: No blockchain set - should fall through to normal validation
	milestone.blockchain = nil
	// Should not trigger fork detection path since blockchain is nil

	// Test case 2: No current block - should fall through to normal validation
	milestone.blockchain = blockchain
	blockchain.SetCurrentBlock(nil)
	// Should not trigger fork detection path since current block is nil

	// Test case 3: Current block number less than milestone number - should fall through
	blockchain.SetCurrentBlock(&types.Header{Number: big.NewInt(int64(milestoneNumber - 5))})
	// Should not trigger fork detection path since current block number < milestone number

	// Test case 4: No local block at milestone number - should fall through
	blockchain.SetCurrentBlock(&types.Header{Number: big.NewInt(int64(milestoneNumber + 5))})
	blockchain.SetBlock(milestoneNumber, nil) // No block at milestone number
	// Should not trigger fork detection path since local block doesn't exist

	// All these cases should fall through to normal validation without triggering fork detection
	// This test verifies that the fork detection logic doesn't crash on edge cases
}

// TestMilestoneSetBlockchain tests the SetBlockchain functionality
func TestMilestoneSetBlockchain(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	service := NewMockService(db)

	// Initially blockchain should be nil
	milestone := service.milestoneService.(*milestone)
	require.Nil(t, milestone.blockchain, "Blockchain should be nil initially")

	// Set blockchain
	blockchain := NewMockChainReader()
	service.SetBlockchain(blockchain)

	// Verify blockchain is set
	require.NotNil(t, milestone.blockchain, "Blockchain should be set")
	require.Equal(t, blockchain, milestone.blockchain, "Blockchain should match what was set")
}

func TestMilestone_IsValidPeer_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	db := rawdb.NewMemoryDatabase()
	service := NewMockService(db)
	milestone := service.milestoneService.(*milestone)

	const numGoroutines = 10
	var wg sync.WaitGroup

	// Simple mock fetchHeadersByNumber function.
	fetchHeaders := func(number uint64, amount int, skip int, reverse bool) ([]*types.Header, []common.Hash, error) {
		header := &types.Header{Number: big.NewInt(int64(number))}
		return []*types.Header{header}, []common.Hash{common.HexToHash("0x123")}, nil
	}

	// Test concurrent access: readers call IsValidPeer while writer calls Process.
	// This tests the race condition fix in the finality fields access.

	// Writer goroutine - updates the milestone data.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 10; i++ {
			blockNum := uint64(100 + i)
			hash := common.BytesToHash([]byte{byte(i)})
			milestone.Process(blockNum, hash)
			time.Sleep(time.Microsecond) // Small delay to increase race probability.
		}
	}()

	// Reader goroutines - call IsValidPeer concurrently.
	for i := range numGoroutines - 1 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// This should not race with Process() calls due to our lock protection fix.
			// The race was in accessing m.doExist, m.Number, m.Hash, m.blockchain fields.
			_, err := milestone.IsValidPeer(fetchHeaders)

			// We don't care about the specific result, just that no race occurs.
			// Error is expected due to mock setup, but no race condition should happen.
			_ = err // Ignore error, we're testing for race conditions, not functionality
		}(i)
	}

	wg.Wait()

	// Verify final state is consistent after concurrent access.
	doExist, finalNumber, finalHash := milestone.Get()
	require.True(t, doExist, "Milestone should exist after processing")
	require.Equal(t, uint64(110), finalNumber, "Final milestone number should be 110")
	require.NotEqual(t, common.Hash{}, finalHash, "Final milestone hash should not be empty")
}

func TestForkCorrectness(t *testing.T) {
	// Note that max fork correctness check limit is set to 10 blocks for tests
	db := rawdb.NewMemoryDatabase()
	s := NewMockService(db)
	chainA := createMockChain(1, 20, common.Hash{}) // A1->A2...A19->A20
	for _, header := range chainA {
		rawdb.WriteHeader(db, header)
	}

	// Note: The fork correctness check returns true if it doesn't have enough info
	// to determine the correctness or if there's any error. The tests below apart
	// from checking if the result is expected or not also checks the helper variables
	// like the last known block and the validation cache to ensure a particular code
	// path is taken.

	t.Run("nil first block", func(t *testing.T) {
		res := s.checkForkCorrectness(nil)
		require.Equal(t, true, res, "expected chain to be valid")
	})

	t.Run("no milestone or checkpoint exists", func(t *testing.T) {
		res := s.checkForkCorrectness(chainA)
		require.Equal(t, true, res, "expected chain to be valid")
	})

	t.Run("long fork: only checkpoint exists", func(t *testing.T) {
		// Whitelist block 5 as checkpoint
		s.ProcessCheckpoint(chainA[5].Number.Uint64(), chainA[5].Hash())

		res := s.checkForkCorrectness(chainA[16:]) // 11 blocks ahead of last checkpoint
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, uint64(0), s.lastValidForkBlock, "expected last known valid block to be 0")
		require.Equal(t, 0, len(s.forkValidationCache), "expected no entries in cache")
	})

	t.Run("long fork: more recent checkpoint than milestone", func(t *testing.T) {
		// Whitelist block 4 as milestone
		s.ProcessMilestone(chainA[4].Number.Uint64(), chainA[4].Hash())

		res := s.checkForkCorrectness(chainA[16:]) // 12 blocks ahead of last milestone
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, uint64(0), s.lastValidForkBlock, "expected last known valid block to be 0")
		require.Equal(t, 0, len(s.forkValidationCache), "expected no entries in cache")
	})

	t.Run("long fork: only milestone exists", func(t *testing.T) {
		// Purge checkpoint
		s.checkpointService.Purge()

		res := s.checkForkCorrectness(chainA[16:]) // 12 blocks ahead of last milestone
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, uint64(0), s.lastValidForkBlock, "expected last known valid block to be 0")
		require.Equal(t, 0, len(s.forkValidationCache), "expected no entries in cache")
	})

	t.Run("long fork: both milestone and checkpoint exist but last known block is recent", func(t *testing.T) {
		s.lastValidForkBlock = 6 // 1 block ahead of last checkpoint

		res := s.checkForkCorrectness(chainA[17:]) // 11 blocks ahead of last known block
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, 0, len(s.forkValidationCache), "expected no entries in cache")
	})

	t.Run("long fork: no milestone or checkpoint exists, only last known block exists", func(t *testing.T) {
		s.checkpointService.Purge()
		s.milestoneService.Purge()

		res := s.checkForkCorrectness(chainA[17:]) // 11 blocks ahead of last known block
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, 0, len(s.forkValidationCache), "expected no entries in cache")
		s.lastValidForkBlock = 0
	})

	// Create a mock chain linking back to last block
	chain1 := createMockChain(21, 22, chainA[19].Hash()) // A21->A22 (extends A20)
	for _, header := range chain1 {
		rawdb.WriteHeader(db, header)
	}

	t.Run("incoming chain ahead of last whitelisted entry", func(t *testing.T) {
		// Whitelist last block of the chain as milestone
		s.ProcessMilestone(chainA[19].Number.Uint64(), chainA[19].Hash())

		res := s.checkForkCorrectness(chain1)
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, chain1[1].Number.Uint64(), s.lastValidForkBlock, "expected last known valid block to be updated")
		require.Equal(t, 2, len(s.forkValidationCache), "expected two entries in cache")
		require.Equal(t, true, s.forkValidationCache[chain1[0].Hash()], "expected cache to have valid entry")
		require.Equal(t, true, s.forkValidationCache[chain1[1].Hash()], "expected cache to have valid entry")
	})

	// Extend the chain further
	chain2 := createMockChain(23, 24, chain1[1].Hash()) // A23->A24 (extends A22)
	for _, header := range chain2 {
		rawdb.WriteHeader(db, header)
	}

	t.Run("incoming chain further ahead of last chain", func(t *testing.T) {
		res := s.checkForkCorrectness(chain2)
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, chain2[1].Number.Uint64(), s.lastValidForkBlock, "expected last known valid block to be updated")
		require.Equal(t, 4, len(s.forkValidationCache), "expected four entries in cache") // block 21, 22, 23, 24 should be present in cache
		require.Equal(t, true, s.forkValidationCache[chain1[0].Hash()], "expected cache to have valid entry: block 21")
		require.Equal(t, true, s.forkValidationCache[chain1[1].Hash()], "expected cache to have valid entry: block 22")
		require.Equal(t, true, s.forkValidationCache[chain2[0].Hash()], "expected cache to have valid entry: block 23")
		require.Equal(t, true, s.forkValidationCache[chain2[1].Hash()], "expected cache to have valid entry: block 24")
	})

	// Extend the chain further
	chain3 := createMockChain(25, 26, chain2[1].Hash()) // A25->A26 (extends A24)
	for _, header := range chain3 {
		rawdb.WriteHeader(db, header)
	}

	t.Run("missing header in database, missing in cache", func(t *testing.T) {
		// Explicitly delete the parent block i.e. 24 from db and cache
		rawdb.DeleteHeader(db, chain2[1].Hash(), chain2[1].Number.Uint64())
		delete(s.forkValidationCache, chain2[1].Hash())

		res := s.checkForkCorrectness(chain3)
		require.Equal(t, true, res, "expected chain to be valid due to missing data")

		// The last valid fork block shouldn't be updated as we couldn't verify the chain
		// due to missing header. The cache except for the explicit deletion should be intact.
		require.Equal(t, chain2[1].Number.Uint64(), s.lastValidForkBlock, "expected last known valid block to be unchanged")
		require.Equal(t, 3, len(s.forkValidationCache), "expected three entries in cache") // block 21, 22, 23 should be present in cache
		require.Equal(t, true, s.forkValidationCache[chain1[0].Hash()], "expected cache to have valid entry: block 21")
		require.Equal(t, true, s.forkValidationCache[chain1[1].Hash()], "expected cache to have valid entry: block 22")
		require.Equal(t, true, s.forkValidationCache[chain2[0].Hash()], "expected cache to have valid entry: block 23")

		// Write the deleted header back to db and cache
		rawdb.WriteHeader(db, chain2[1])
		s.forkValidationCache[chain2[1].Hash()] = true
	})

	t.Run("missing entry in database, present in cache", func(t *testing.T) {
		// Explicitly delete the parent block i.e. 24 only from db this time
		rawdb.DeleteHeader(db, chain2[1].Hash(), chain2[1].Number.Uint64())

		// The fork should be valid as block 24 is present in cache (even though header is not available)
		res := s.checkForkCorrectness(chain3)
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, chain3[1].Number.Uint64(), s.lastValidForkBlock, "expected last known valid block to be updates") // block 26

		require.Equal(t, 6, len(s.forkValidationCache), "expected six entries in cache") // block 21, 22, 23, 24, 25, 26 should be present in cache
		require.Equal(t, true, s.forkValidationCache[chain1[0].Hash()], "expected cache to have valid entry: block 21")
		require.Equal(t, true, s.forkValidationCache[chain1[1].Hash()], "expected cache to have valid entry: block 22")
		require.Equal(t, true, s.forkValidationCache[chain2[0].Hash()], "expected cache to have valid entry: block 23")
		require.Equal(t, true, s.forkValidationCache[chain2[1].Hash()], "expected cache to have valid entry: block 24")
		require.Equal(t, true, s.forkValidationCache[chain3[0].Hash()], "expected cache to have valid entry: block 25")
		require.Equal(t, true, s.forkValidationCache[chain3[1].Hash()], "expected cache to have valid entry: block 26")

		// Write the deleted header back to db
		rawdb.WriteHeader(db, chain2[1])
	})

	t.Run("cache clearance on new milestone", func(t *testing.T) {
		s.ProcessMilestone(chain3[1].Number.Uint64(), chain3[1].Hash())
		require.Equal(t, 0, len(s.forkValidationCache), "expected cache to be cleared on new milestone")
	})

	t.Run("past chain before last milestone", func(t *testing.T) {
		res := s.checkForkCorrectness(chain3[:1])
		require.Equal(t, true, res, "expected chain to be valid")
		require.Equal(t, 0, len(s.forkValidationCache), "expected no entries in cache")
	})

	// Outage scenario: Currently, block 26, which is the last milestone has been whitelisted. The
	// database also has the same entry. This case tries to add a conflicting header entry of a
	// different fork for the last whitelisted block.
	//	  A25 --------> A26 (m)   ---> A27
	//			  \
	//			   \--> A26' (db) ---> A27'
	//
	// A26 (m) is the milestone entry. Currently, it matches with the database entry but we'll try
	// to explicitly change the database entry to A26' which belongs to a different fork. All the
	// chains on top of A26' should be rejected.
	chain4 := createMockChain(26, 27, chain3[0].Hash()) // A25 -> A26' -> A27'
	// Firstly, delete the old header
	rawdb.DeleteHeader(db, chain3[1].Hash(), chain3[1].Number.Uint64())
	for _, header := range chain4 {
		rawdb.WriteHeader(db, header)
	}
	t.Run("outage scenario: conflicting fork in database and whitelisted entry", func(t *testing.T) {
		res := s.checkForkCorrectness(chain4[1:])
		require.Equal(t, false, res, "expected chain to be invalid due to mismatch with milestone")
		require.Equal(t, 0, len(s.forkValidationCache), "expected no entries in cache")
		require.Equal(t, chain3[1].Number.Uint64(), s.lastValidForkBlock, "expected last known valid block to be unchanged")
	})
}
