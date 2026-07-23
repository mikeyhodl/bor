package bor

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/statefull"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

// gasBoundBorConfig returns an Indore configuration with both state-sync budgets enabled.
func gasBoundBorConfig(gasBoundBlock *big.Int) *params.BorConfig {
	cfg := indoreBorConfig()
	cfg.ValenciaBlock = big.NewInt(0)
	cfg.StateSyncGasBoundBlock = gasBoundBlock
	cfg.StateReceiverContract = "0x0000000000000000000000000000000000001001"
	return cfg
}

// gasBoundEvents builds a contiguous state-sync backlog accepted by validateEventRecord.
func gasBoundEvents(n int, eventTime time.Time) []*clerk.EventRecordWithTime {
	events := make([]*clerk.EventRecordWithTime, n)
	for i := range events {
		events[i] = &clerk.EventRecordWithTime{
			EventRecord: clerk.EventRecord{
				ID:       uint64(i + 1),
				Contract: common.HexToAddress("0x1001"),
				Data:     []byte{0x01},
				ChainID:  "1",
			},
			Time: eventTime,
		}
	}
	return events
}

// TestCommitStates_StateSyncGasBudget verifies the threshold, progress guarantee,
// zero-gas behavior, and unchanged pre-activation behavior.
func TestCommitStates_StateSyncGasBudget(t *testing.T) {
	t.Parallel()
	const numEvents = 20
	budget := params.MaxStateSyncGasPerBlock

	// The threshold is checked before the next record. The record that reaches or
	// crosses it remains included, and any following record is deferred.
	cases := []struct {
		name          string
		gasBoundBlock *big.Int
		gasUsed       uint64
		want          int
	}{
		{"crosses threshold", big.NewInt(0), 4_000_000, 8},
		{"exact threshold", big.NewInt(0), 5_000_000, 6},
		{"single record exceeds threshold", big.NewInt(0), budget + 1, 1},
		{"zero gas", big.NewInt(0), 0, numEvents},
		{"before activation", big.NewInt(17), 5_000_000, numEvents},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := common.HexToAddress("0x1")
			sp := &fakeSpanner{vals: []*valset.Validator{{Address: addr, VotingPower: 1}}}
			chain, b := newChainAndBorForTest(t, sp, gasBoundBorConfig(tc.gasBoundBlock), true, addr, uint64(time.Now().Unix())-200)
			b.GenesisContractsClient = &mockGenesisContractForCommitStatesIndore{lastStateID: 0, gasUsed: tc.gasUsed}

			now := time.Now()
			b.SetHeimdallClient(&mockHeimdallClient{
				span: &borTypes.Span{
					Id: 0, StartBlock: 0, EndBlock: 255, BorChainId: "1",
					ValidatorSet:      stakeTypes.ValidatorSet{Validators: []*stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}}},
					SelectedProducers: []stakeTypes.Validator{{ValId: 1, Signer: addr.Hex(), VotingPower: 1}},
				},
				events: gasBoundEvents(numEvents, now.Add(-60*time.Second)),
			})

			hc := chain.HeaderChain()
			genesis := hc.GetHeaderByNumber(0)
			statedb := newStateDBForTest(t, genesis.Root)
			h := &types.Header{Number: big.NewInt(16), ParentHash: genesis.Hash(), Time: uint64(now.Unix())}

			result, err := b.CommitStates(statedb, h, statefull.ChainContext{Chain: hc, Bor: b})
			require.NoError(t, err)
			// CommitStates must return exactly the contiguous prefix admitted by the budget.
			require.Len(t, result, tc.want)
		})
	}
}

// stateSyncContextContract observes the transaction-scoped state presented to
// each callback and then repopulates it for the following callback.
type stateSyncContextContract struct {
	t              *testing.T
	gasUsed        uint64
	calls          int
	wantPrepared   bool
	stateReceiver  common.Address
	staleAddress   common.Address
	transientKey   common.Hash
	transientValue common.Hash
}

func (m *stateSyncContextContract) CommitState(_ *clerk.EventRecordWithTime, state vm.StateDB, _ *types.Header, _ statefull.ChainContext, _ vm.Config) (uint64, error) {
	m.t.Helper()
	if m.wantPrepared {
		// Preparation removes stale state while warming the canonical sender and destination.
		require.False(m.t, state.AddressInAccessList(m.staleAddress))
		require.True(m.t, state.AddressInAccessList(params.BorSystemAddress))
		require.True(m.t, state.AddressInAccessList(m.stateReceiver))
		require.Zero(m.t, state.GetTransientState(m.staleAddress, m.transientKey))
	} else {
		// Before activation, CommitStates preserves the historical transaction context.
		require.True(m.t, state.AddressInAccessList(m.staleAddress))
		require.Equal(m.t, m.transientValue, state.GetTransientState(m.staleAddress, m.transientKey))
	}

	// Reintroduce stale values so the next callback proves preparation occurs per record.
	state.AddAddressToAccessList(m.staleAddress)
	state.SetTransientState(m.staleAddress, m.transientKey, m.transientValue)
	m.calls++
	return m.gasUsed, nil
}

func (m *stateSyncContextContract) LastStateId(*state.StateDB, uint64, common.Hash) (*big.Int, error) {
	return new(big.Int), nil
}

// prepareTrackingState records every Prepare call while preserving StateDB behavior.
type prepareTrackingState struct {
	vm.StateDB
	calls []params.Rules
}

func (s *prepareTrackingState) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, accesses types.AccessList) {
	s.calls = append(s.calls, rules)
	s.StateDB.Prepare(rules, sender, coinbase, dest, precompiles, accesses)
}

// TestCommitStates_StateSyncContextPreparation verifies that only admitted
// post-activation records receive a fresh transaction context.
func TestCommitStates_StateSyncContextPreparation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		gasBoundBlock    *big.Int
		gasUsed          uint64
		events           int
		wantIncluded     int
		wantPrepareCalls int
		wantPrepared     bool
	}{
		{"before activation", big.NewInt(17), 1, 2, 2, 0, false},
		{"each admitted record", big.NewInt(0), params.MaxStateSyncGasPerBlock / 4, 2, 2, 2, true},
		{"deferred record", big.NewInt(0), params.MaxStateSyncGasPerBlock + 1, 2, 1, 1, true},
		{"empty backlog", big.NewInt(0), 1, 0, 0, 0, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			now := time.Now()
			signer := common.HexToAddress("0x1")
			stateReceiver := common.HexToAddress("0x1001")
			staleAddress := common.HexToAddress("0x2002")
			transientKey := common.HexToHash("0x01")
			transientValue := common.HexToHash("0x02")
			sp := &fakeSpanner{vals: []*valset.Validator{{Address: signer, VotingPower: 1}}}
			borConfig := gasBoundBorConfig(test.gasBoundBlock)
			chainConfig := newAllForksChainConfig(borConfig)
			chain, b := newChainAndBorForTestWithConfig(t, sp, chainConfig, true, signer, uint64(now.Unix())-200)
			contract := &stateSyncContextContract{
				t:              t,
				gasUsed:        test.gasUsed,
				wantPrepared:   test.wantPrepared,
				stateReceiver:  stateReceiver,
				staleAddress:   staleAddress,
				transientKey:   transientKey,
				transientValue: transientValue,
			}
			b.GenesisContractsClient = contract
			b.SetHeimdallClient(&mockHeimdallClient{events: gasBoundEvents(test.events, now.Add(-60*time.Second))})

			chainContext := statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b}
			genesis := chainContext.GetHeaderByNumber(0)
			statedb := newStateDBForTest(t, genesis.Root)
			rootBefore := statedb.IntermediateRoot(false)
			// Seed transaction-scoped values and a prior log. Prepare should clear only
			// the transaction-scoped values and leave consensus state intact.
			existingLog := &types.Log{Address: staleAddress, Data: []byte{0x01}}
			statedb.AddLog(existingLog)
			statedb.AddAddressToAccessList(staleAddress)
			statedb.SetTransientState(staleAddress, transientKey, transientValue)
			trackedState := &prepareTrackingState{StateDB: statedb}
			header := &types.Header{Number: big.NewInt(16), ParentHash: genesis.Hash(), Time: uint64(now.Unix())}

			result, err := b.CommitStates(trackedState, header, chainContext)
			require.NoError(t, err)
			// Each included record executes exactly once and each eligible execution
			// receives exactly one Prepare call.
			require.Len(t, result, test.wantIncluded)
			require.Equal(t, test.wantIncluded, contract.calls)
			require.Len(t, trackedState.calls, test.wantPrepareCalls)
			for _, rules := range trackedState.calls {
				// Bor state-sync calls use non-merge execution rules.
				require.False(t, rules.IsMerge)
			}
			// Prepare must not remove existing logs or modify the persistent state root.
			require.Equal(t, []*types.Log{existingLog}, statedb.Logs())
			require.Equal(t, rootBefore, statedb.IntermediateRoot(false))
		})
	}
}

// accessListSensitiveStateSyncContract executes a real SLOAD through the system-call
// path. gasBias positions cumulative gas around the block threshold without changing
// the warm-versus-cold gas delta produced by the EVM.
type accessListSensitiveStateSyncContract struct {
	chainConfig *params.ChainConfig
	target      common.Address
	gasBias     uint64
}

func (c *accessListSensitiveStateSyncContract) CommitState(_ *clerk.EventRecordWithTime, state vm.StateDB, header *types.Header, chain statefull.ChainContext, config vm.Config) (uint64, error) {
	message := statefull.GetSystemMessage(c.target, nil)
	gasUsed, err := statefull.ApplyMessage(context.Background(), message, state, header, c.chainConfig, chain, config)
	return c.gasBias + gasUsed, err
}

func (*accessListSensitiveStateSyncContract) LastStateId(*state.StateDB, uint64, common.Hash) (*big.Int, error) {
	return new(big.Int), nil
}

// TestCommitStates_SerialAndBlockSTMStateSyncParity executes the same EIP-2930
// transaction through serial execution and V2 BlockSTM. Serial execution retains
// its access list, while BlockSTM settlement copies persistent writes but not the
// transaction-scoped access list. CommitStates must normalize that difference before
// gas accounting so both paths admit the same state-sync prefix.
func TestCommitStates_SerialAndBlockSTMStateSyncParity(t *testing.T) {
	t.Parallel()

	now := time.Now()
	target := common.HexToAddress("0x1001")
	slot := common.Hash{}
	signerAddress := common.HexToAddress("0x1")
	borConfig := gasBoundBorConfig(big.NewInt(0))
	borConfig.BurntContract = map[string]string{"0": common.Address{}.Hex()}
	borConfig.ValidatorContract = common.HexToAddress("0x1000").Hex()
	chainConfig := newAllForksChainConfig(borConfig)
	sp := &fakeSpanner{vals: []*valset.Validator{{Address: signerAddress, VotingPower: 1}}}
	chain, b := newChainAndBorForTestWithConfig(t, sp, chainConfig, true, signerAddress, uint64(now.Unix())-200)
	b.SetHeimdallClient(&mockHeimdallClient{events: gasBoundEvents(3, now.Add(-60*time.Second))})

	memoryDB := rawdb.NewMemoryDatabase()
	trieDB := triedb.NewDatabase(memoryDB, triedb.HashDefaults)
	stateDatabase := state.NewDatabase(trieDB, nil)
	initialState, err := state.New(common.Hash{}, stateDatabase)
	require.NoError(t, err)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	initialState.AddBalance(sender, uint256.NewInt(params.Ether), tracing.BalanceChangeUnspecified)
	// The target loads slot zero, discards it, and returns uint256(1).
	initialState.SetCode(target, common.FromHex("0x60005450600160005260206000f3"), tracing.CodeChangeUnspecified)
	initialState.SetState(target, slot, common.HexToHash("0x01"))
	root, err := initialState.Commit(0, false, false)
	require.NoError(t, err)
	require.NoError(t, trieDB.Commit(root, false))

	newState := func() *state.StateDB {
		statedb, err := state.New(root, stateDatabase)
		require.NoError(t, err)
		return statedb
	}

	header := &types.Header{
		Number:     big.NewInt(16),
		ParentHash: chain.HeaderChain().GetHeaderByNumber(0).Hash(),
		Coinbase:   common.HexToAddress("0xc0ffee"),
		Difficulty: big.NewInt(1),
		GasLimit:   params.MaxGasLimit,
		BaseFee:    new(big.Int),
		Time:       uint64(now.Unix()),
	}
	chainContext := statefull.ChainContext{Chain: chain.HeaderChain(), Bor: b}
	blockContext := core.NewEVMBlockContext(header, chainContext, &header.Coinbase)
	transactionSigner := types.MakeSigner(chainConfig, header.Number, header.Time)
	// The final user transaction explicitly warms the target and slot zero.
	transaction, err := types.SignTx(types.NewTx(&types.AccessListTx{
		ChainID:    chainConfig.ChainID,
		GasPrice:   big.NewInt(1),
		Gas:        100_000,
		To:         &target,
		Value:      new(big.Int),
		AccessList: types.AccessList{{Address: target, StorageKeys: []common.Hash{slot}}},
	}), transactionSigner, key)
	require.NoError(t, err)
	message, err := core.TransactionToMessage(transaction, transactionSigner, header.BaseFee)
	require.NoError(t, err)

	// Execute the access-list transaction through the production serial path.
	serialState := newState()
	serialState.SetTxContext(transaction.Hash(), 0)
	serialEVM := vm.NewEVM(blockContext, serialState, chainConfig, vm.Config{})
	var serialGasUsed uint64
	_, err = core.ApplyTransactionWithEVM(message, new(core.GasPool).AddGas(header.GasLimit), serialState, header.Number, header.Hash(), header.Time, transaction, &serialGasUsed, serialEVM)
	require.NoError(t, err)

	// Execute and settle the same transaction through the production V2 BlockSTM path.
	blockSTMBase := newState()
	blockSTMState := newState()
	result := core.ExecuteV2BlockSTM(
		context.Background(),
		[]core.V2Task{{Index: 0, Tx: transaction, Msg: message}},
		blockSTMBase,
		blockstm.NewMVStore(),
		blockstm.NewMVBalanceStore(),
		blockContext,
		header.Hash(),
		vm.Config{},
		chainConfig,
		header.GasLimit,
		2,
		blockSTMState,
		nil,
	)
	// The BlockSTM executor must settle the transaction without an execution failure.
	require.Equal(t, -1, result.PanickedIdx)
	require.Equal(t, -1, result.ExecErrIdx)
	// Both processors must agree on persistent state before Bor finalization.
	require.Equal(t, serialState.Copy().IntermediateRoot(true), blockSTMState.Copy().IntermediateRoot(true))

	// The fixture must expose the transaction-scoped difference that Prepare normalizes.
	_, serialSlotWarm := serialState.SlotInAccessList(target, slot)
	_, blockSTMSlotWarm := blockSTMState.SlotInAccessList(target, slot)
	require.True(t, serialSlotWarm)
	require.False(t, blockSTMSlotWarm)

	// Probe copies before normalization to measure the real EVM warm/cold SLOAD delta.
	stateSyncContract := &accessListSensitiveStateSyncContract{chainConfig: chainConfig, target: target}
	warmGas, err := stateSyncContract.CommitState(nil, serialState.Copy(), header, chainContext, vm.Config{})
	require.NoError(t, err)
	coldGas, err := stateSyncContract.CommitState(nil, blockSTMState.Copy(), header, chainContext, vm.Config{})
	require.NoError(t, err)
	require.Greater(t, coldGas, warmGas)

	// Position two callbacks on opposite sides of the threshold without preparation:
	// serial remains below it, while BlockSTM reaches it because its first SLOAD is cold.
	stateSyncContract.gasBias = (params.MaxStateSyncGasPerBlock - 1 - 2*warmGas) / 2
	serialWithoutPreparation := 2 * (stateSyncContract.gasBias + warmGas)
	blockSTMWithoutPreparation := serialWithoutPreparation + coldGas - warmGas
	require.Less(t, serialWithoutPreparation, params.MaxStateSyncGasPerBlock)
	require.GreaterOrEqual(t, blockSTMWithoutPreparation, params.MaxStateSyncGasPerBlock)
	b.GenesisContractsClient = stateSyncContract

	// Per-record preparation makes both callbacks cold in both processors. Each path
	// therefore admits two records. Removing Prepare makes serial admit three and
	// BlockSTM admit two, causing the prefix-length assertion to fail.
	serialStateSyncs, err := b.CommitStates(serialState, header, chainContext)
	require.NoError(t, err)
	blockSTMStateSyncs, err := b.CommitStates(blockSTMState, header, chainContext)
	require.NoError(t, err)
	require.Equal(t, len(serialStateSyncs), len(blockSTMStateSyncs), "state-sync prefix length")
	require.Equal(t, serialStateSyncs, blockSTMStateSyncs)
	require.Len(t, serialStateSyncs, 2)
}
