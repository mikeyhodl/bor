package bor

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/contract"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

// recordingGenesisContract uses the production call path and captures cumulative gas.
type recordingGenesisContract struct {
	client  *contract.GenesisContractsClient
	gasUsed uint64
}

func (c *recordingGenesisContract) CommitState(event *clerk.EventRecordWithTime, state vm.StateDB, header *types.Header, chain statefull.ChainContext, config vm.Config) (uint64, error) {
	gasUsed, err := c.client.CommitState(event, state, header, chain, config)
	if err != nil {
		return 0, err
	}
	c.gasUsed += gasUsed
	return gasUsed, nil
}

func (*recordingGenesisContract) LastStateId(*state.StateDB, uint64, common.Hash) (*big.Int, error) {
	return new(big.Int), nil
}

type stateSyncProcessorFixture struct {
	chain         *core.BlockChain
	stateDatabase *state.CachingDB
	root          common.Hash
	block         *types.Block
	stateSyncTx   *types.Transaction
	target        common.Address
	author        common.Address
}

func forwardingContractCode(target common.Address) []byte {
	code := common.FromHex("0x6000600060006000600073")
	code = append(code, target.Bytes()...)
	return append(code, common.FromHex("0x5af150600160005260206000f3")...)
}

func gasThresholdContractCode(threshold uint64) []byte {
	code := append([]byte{byte(vm.PUSH32)}, common.BigToHash(new(big.Int).SetUint64(threshold)).Bytes()...)
	// Store and log whether the forwarded gas exceeds the calibrated threshold.
	return append(code, common.FromHex("0x5a118060005560005260206000a0600160005260206000f3")...)
}

func commitStateDatabase(t *testing.T, code map[common.Address][]byte, funded common.Address) (*state.CachingDB, common.Hash) {
	t.Helper()
	memoryDB := rawdb.NewMemoryDatabase()
	trieDB := triedb.NewDatabase(memoryDB, triedb.HashDefaults)
	stateDatabase := state.NewDatabase(trieDB, nil)
	statedb, err := state.New(common.Hash{}, stateDatabase)
	require.NoError(t, err)
	for address, bytecode := range code {
		statedb.SetCode(address, bytecode, tracing.CodeChangeUnspecified)
	}
	statedb.AddBalance(funded, uint256.NewInt(params.Ether), tracing.BalanceChangeUnspecified)
	root, err := statedb.Commit(0, false, false)
	require.NoError(t, err)
	require.NoError(t, trieDB.Commit(root, false))
	return stateDatabase, root
}

func warmStateSyncPath(statedb *state.StateDB, addresses ...common.Address) {
	for _, address := range addresses {
		statedb.AddAddressToAccessList(address)
	}
}

func measureForwardedGas(t *testing.T, stateDatabase *state.CachingDB, root common.Hash, client *contract.GenesisContractsClient, event *clerk.EventRecordWithTime, header *types.Header, chainContext statefull.ChainContext, target common.Address, warmAddresses ...common.Address) uint64 {
	t.Helper()
	statedb, err := state.New(root, stateDatabase)
	require.NoError(t, err)
	warmStateSyncPath(statedb, warmAddresses...)
	_, err = client.CommitState(event, statedb, header, chainContext, vm.Config{})
	require.NoError(t, err)
	return statedb.GetState(target, common.Hash{}).Big().Uint64()
}

func newStateSyncProcessorFixture(t *testing.T) *stateSyncProcessorFixture {
	t.Helper()
	now := time.Now()
	stateReceiver := common.HexToAddress("0x1001")
	fxChild := common.HexToAddress("0x2001")
	target := common.HexToAddress("0x3001")
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	author := crypto.PubkeyToAddress(key.PublicKey)
	borConfig := gasBoundBorConfig(big.NewInt(0))
	borConfig.MadhugiriBlock = big.NewInt(0)
	borConfig.RioBlock = big.NewInt(0)
	borConfig.BurntContract = map[string]string{"0": common.Address{}.Hex()}
	borConfig.ValidatorContract = common.HexToAddress("0x1000").Hex()
	chainConfig := newAllForksChainConfig(borConfig)
	sp := &fakeSpanner{vals: []*valset.Validator{{Address: author, VotingPower: 1}}}
	chain, b := newChainAndBorForTestWithConfig(t, sp, chainConfig, true, author, uint64(now.Unix())-200)
	header := newStateSyncParityHeader(chain, author, now)
	event := &clerk.EventRecordWithTime{EventRecord: clerk.EventRecord{ID: 1, Contract: fxChild, Data: []byte("payload"), ChainID: "1"}, Time: now.Add(-60 * time.Second)}
	b.SetHeimdallClient(&mockHeimdallClient{events: []*clerk.EventRecordWithTime{event}})
	client := contract.NewGenesisContractsClient(chainConfig, borConfig.ValidatorContract, borConfig.StateReceiverContract, nil)
	b.GenesisContractsClient = &recordingGenesisContract{client: client}
	chainContext := statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b}
	// The deployed StateReceiver calls the FxChild fixture, which forwards to the
	// gas-sensitive receiver used to distinguish warm and cold execution.
	stateReceiverCode := core.DefaultBorMainnetGenesisBlock().Alloc[stateReceiver].Code
	probeCode := common.FromHex("0x5a600055600160005260206000f3")
	probeDB, probeRoot := commitStateDatabase(t, map[common.Address][]byte{stateReceiver: stateReceiverCode, fxChild: forwardingContractCode(target), target: probeCode}, author)
	coldGas := measureForwardedGas(t, probeDB, probeRoot, client, event, header, chainContext, target)
	warmGas := measureForwardedGas(t, probeDB, probeRoot, client, event, header, chainContext, target, stateReceiver, fxChild, target)
	require.Greater(t, warmGas, coldGas)
	threshold := coldGas + (warmGas-coldGas)/2
	require.Greater(t, threshold, uint64(3))
	threshold -= 3 // PUSH32 executes before GAS in the final receiver.
	stateDatabase, root := commitStateDatabase(t, map[common.Address][]byte{stateReceiver: stateReceiverCode, fxChild: forwardingContractCode(target), target: gasThresholdContractCode(threshold)}, author)
	userTx := newWarmPathTransaction(t, key, chainConfig, stateReceiver, fxChild, target)
	stateSyncTx := types.NewTx(&types.StateSyncTx{StateSyncData: []*types.StateSyncData{{ID: event.ID, Contract: event.Contract, Data: event.Data, TxHash: event.TxHash}}})
	block := types.NewBlock(header, &types.Body{Transactions: []*types.Transaction{userTx, stateSyncTx}}, nil, trie.NewStackTrie(nil))
	return &stateSyncProcessorFixture{chain: chain, stateDatabase: stateDatabase, root: root, block: block, stateSyncTx: stateSyncTx, target: target, author: author}
}

func newStateSyncParityHeader(chain *core.BlockChain, author common.Address, now time.Time) *types.Header {
	return &types.Header{
		Number:     big.NewInt(16),
		ParentHash: chain.HeaderChain().GetHeaderByNumber(0).Hash(),
		Coinbase:   author,
		Difficulty: big.NewInt(1),
		GasLimit:   params.MaxGasLimit,
		BaseFee:    new(big.Int),
		Time:       uint64(now.Unix()),
	}
}

func newWarmPathTransaction(t *testing.T, key *ecdsa.PrivateKey, chainConfig *params.ChainConfig, addresses ...common.Address) *types.Transaction {
	t.Helper()
	destination := common.HexToAddress("0x4001")
	accessList := make(types.AccessList, len(addresses))
	for i, address := range addresses {
		accessList[i] = types.AccessTuple{Address: address, StorageKeys: []common.Hash{{}}}
	}
	tx, err := types.SignTx(types.NewTx(&types.AccessListTx{
		ChainID: chainConfig.ChainID, GasPrice: new(big.Int), Gas: 100_000, To: &destination, AccessList: accessList,
	}), types.MakeSigner(chainConfig, big.NewInt(16), 0), key)
	require.NoError(t, err)
	return tx
}

func processStateSyncBlock(t *testing.T, fixture *stateSyncProcessorFixture, parallel bool) (*core.ProcessResult, *state.StateDB) {
	t.Helper()
	statedb, err := state.New(fixture.root, fixture.stateDatabase)
	require.NoError(t, err)
	var processor core.Processor = core.NewStateProcessor(fixture.chain)
	if parallel {
		processor = core.NewV2StateProcessor(fixture.chain, fixture.chain, 2)
	}
	result, err := processor.Process(fixture.block, statedb, vm.Config{}, &fixture.author, context.Background())
	require.NoError(t, err)
	return result, statedb
}

// TestStateProcessors_StateSyncParity compares the complete serial and V2 block results.
func TestStateProcessors_StateSyncParity(t *testing.T) {
	t.Parallel()
	fixture := newStateSyncProcessorFixture(t)
	serialResult, serialState := processStateSyncBlock(t, fixture, false)
	parallelResult, parallelState := processStateSyncBlock(t, fixture, true)

	require.Equal(t, serialState.Copy().IntermediateRoot(true), parallelState.Copy().IntermediateRoot(true))
	require.Equal(t, serialResult.GasUsed, parallelResult.GasUsed)
	require.Equal(t, serialResult.Receipts, parallelResult.Receipts)
	require.Equal(t, serialResult.Logs, parallelResult.Logs)
	require.Equal(t, types.DeriveSha(serialResult.Receipts, trie.NewStackTrie(nil)), types.DeriveSha(parallelResult.Receipts, trie.NewStackTrie(nil)))
	require.Equal(t, types.MergeBloom(serialResult.Receipts), types.MergeBloom(parallelResult.Receipts))
	require.Len(t, serialResult.Receipts, 2)
	require.Equal(t, fixture.stateSyncTx.Hash(), serialResult.Receipts[1].TxHash)
	require.Equal(t, fixture.stateSyncTx.Hash(), parallelResult.Receipts[1].TxHash)
	require.Len(t, serialResult.Logs, 1)
	require.Equal(t, fixture.target, serialResult.Logs[0].Address)
	require.Zero(t, serialState.GetState(fixture.target, common.Hash{}))
}

// The first invocation stores 0x2a transiently. The second persists TLOAD(0)
// in slot one, exposing whether records share transaction-scoped storage.
var transientStateSyncCode = common.FromHex("0x6000541560175760005c600155600160005260206000f35b6001600055602a60005d600160005260206000f3")

func runTransientStorageParity(t *testing.T, gasBoundBlock *big.Int) common.Hash {
	t.Helper()
	now := time.Now()
	receiver := common.HexToAddress("0x1001")
	author := common.HexToAddress("0x1")
	borConfig := gasBoundBorConfig(gasBoundBlock)
	borConfig.BurntContract = map[string]string{"0": common.Address{}.Hex()}
	borConfig.ValidatorContract = common.HexToAddress("0x1000").Hex()
	chainConfig := newAllForksChainConfig(borConfig)
	chainConfig.ShanghaiBlock = big.NewInt(0)
	chainConfig.CancunBlock = big.NewInt(0)
	require.True(t, chainConfig.Rules(big.NewInt(16), false, uint64(now.Unix())).IsCancun)
	sp := &fakeSpanner{vals: []*valset.Validator{{Address: author, VotingPower: 1}}}
	chain, b := newChainAndBorForTestWithConfig(t, sp, chainConfig, true, author, uint64(now.Unix())-200)
	events := gasBoundEvents(2, now.Add(-60*time.Second))
	b.SetHeimdallClient(&mockHeimdallClient{events: events})
	header := newStateSyncParityHeader(chain, author, now)
	chainContext := statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b}
	stateDatabase, root := commitStateDatabase(t, map[common.Address][]byte{receiver: transientStateSyncCode}, author)
	newState := func() *state.StateDB {
		statedb, err := state.New(root, stateDatabase)
		require.NoError(t, err)
		return statedb
	}

	recorder := &recordingGenesisContract{client: contract.NewGenesisContractsClient(chainConfig, borConfig.ValidatorContract, borConfig.StateReceiverContract, nil)}
	b.GenesisContractsClient = recorder
	liveState := newState()
	stateSyncs, err := b.CommitStates(liveState, header, chainContext)
	require.NoError(t, err)
	require.Len(t, stateSyncs, 2)

	stateSyncTx := types.NewTx(&types.StateSyncTx{StateSyncData: stateSyncs})
	message, err := core.TransactionToMessage(stateSyncTx, types.MakeSigner(chainConfig, header.Number, header.Time), header.BaseFee)
	require.NoError(t, err)
	traceState := newState()
	traceState.SetTxContext(stateSyncTx.Hash(), 0)
	traceEVM := vm.NewEVM(core.NewEVMBlockContext(header, chainContext, &header.Coinbase), traceState, chainConfig, vm.Config{})
	traceResult, err := statefull.ApplyStateSyncEvents(t.Context(), traceEVM, stateSyncTx, message, receiver)
	require.NoError(t, err)
	require.NoError(t, traceResult.Err)
	require.Equal(t, recorder.gasUsed, traceResult.UsedGas)
	observedSlot := common.BigToHash(big.NewInt(1))
	require.Equal(t, common.BigToHash(big.NewInt(1)), liveState.GetState(receiver, common.Hash{}))
	require.Equal(t, liveState.GetState(receiver, observedSlot), traceState.GetState(receiver, observedSlot))
	require.Equal(t, liveState.Copy().IntermediateRoot(true), traceState.Copy().IntermediateRoot(true))
	return liveState.GetState(receiver, observedSlot)
}

// TestCommitStates_TransientStorageTraceParity pins record isolation with real
// Cancun opcodes and verifies that trace replay follows the same fork boundary.
func TestCommitStates_TransientStorageTraceParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		gasBoundBlock *big.Int
		wantObserved  common.Hash
	}{
		{"before activation", big.NewInt(17), common.BigToHash(big.NewInt(0x2a))},
		{"after activation", big.NewInt(0), common.Hash{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.wantObserved, runTransientStorageParity(t, test.gasBoundBlock))
		})
	}
}
