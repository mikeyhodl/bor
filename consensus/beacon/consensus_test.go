// Copyright 2021 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package beacon

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// mockChainReader is a minimal implementation of consensus.ChainHeaderReader for testing
type mockChainReader struct {
	config *params.ChainConfig
}

func (m *mockChainReader) Config() *params.ChainConfig {
	return m.config
}

func (m *mockChainReader) CurrentHeader() *types.Header {
	return &types.Header{}
}

func (m *mockChainReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	return nil
}

func (m *mockChainReader) GetHeaderByNumber(number uint64) *types.Header {
	return nil
}

func (m *mockChainReader) GetHeaderByHash(hash common.Hash) *types.Header {
	return nil
}

func (m *mockChainReader) GetTd(hash common.Hash, number uint64) *big.Int {
	return big.NewInt(0)
}

func TestFinalizeAndAssembleReturnsCommitTime(t *testing.T) {
	t.Parallel()

	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		funds   = big.NewInt(0).Mul(big.NewInt(1000), big.NewInt(params.Ether))
	)

	t.Run("PoS block without withdrawals", func(t *testing.T) {
		config := *params.MergedTestChainConfig

		// Create state database
		db := rawdb.NewMemoryDatabase()
		tdb := triedb.NewDatabase(db, triedb.HashDefaults)
		statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))

		// Fund the test account
		statedb.SetBalance(address, uint256.MustFromBig(funds), tracing.BalanceChangeUnspecified)
		statedb.SetNonce(address, 0, tracing.NonceChangeUnspecified)

		// Create a PoS header (difficulty = 0)
		header := &types.Header{
			Number:     big.NewInt(1),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000000,
			Difficulty: common.Big0, // PoS has zero difficulty
			Coinbase:   common.Address{},
			ParentHash: common.Hash{},
		}

		// Create beacon engine
		engine := New(ethash.NewFaker())
		chain := &mockChainReader{config: &config}

		// Create empty body
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  []*types.Withdrawal{},
		}

		// Call FinalizeAndAssemble
		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})

		// Verify no error occurred
		if err != nil {
			t.Fatalf("FinalizeAndAssemble failed: %v", err)
		}

		// Verify block was created
		if block == nil {
			t.Fatal("FinalizeAndAssemble returned nil block")
		}

		// Verify receipts were returned
		if receipts == nil {
			t.Fatal("FinalizeAndAssemble returned nil receipts")
		}

		// Verify commit time is non-negative (it should be >= 0)
		if commitTime < 0 {
			t.Fatalf("FinalizeAndAssemble returned negative commit time: %v", commitTime)
		}

		// Verify block has correct header values
		if block.Number().Cmp(header.Number) != 0 {
			t.Errorf("Block number mismatch: got %v, want %v", block.Number(), header.Number)
		}

		if block.GasLimit() != header.GasLimit {
			t.Errorf("Block gas limit mismatch: got %v, want %v", block.GasLimit(), header.GasLimit)
		}

		// Verify state root was set (this is the key part that takes time)
		if block.Root() == (common.Hash{}) {
			t.Error("Block root hash is empty")
		}

		if header.Root == (common.Hash{}) {
			t.Error("Header root hash is empty")
		}

		t.Logf("Commit time for PoS block: %v", commitTime)
	})

	t.Run("PoS block with larger state", func(t *testing.T) {
		config := *params.MergedTestChainConfig

		// Create state database
		db := rawdb.NewMemoryDatabase()
		tdb := triedb.NewDatabase(db, triedb.HashDefaults)
		statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))

		// Fund the test account
		statedb.SetBalance(address, uint256.MustFromBig(funds), tracing.BalanceChangeUnspecified)
		statedb.SetNonce(address, 0, tracing.NonceChangeUnspecified)

		// Add multiple storage entries to increase state size
		testAddr := common.Address{0xaa}
		statedb.SetBalance(testAddr, uint256.NewInt(100), tracing.BalanceChangeUnspecified)
		for i := 0; i < 100; i++ {
			key := common.BigToHash(big.NewInt(int64(i)))
			val := common.BigToHash(big.NewInt(int64(i * 2)))
			statedb.SetState(testAddr, key, val)
		}

		// Create a PoS header
		header := &types.Header{
			Number:     big.NewInt(2),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000001,
			Difficulty: common.Big0,
			Coinbase:   common.Address{},
			ParentHash: common.Hash{0x01},
		}

		// Create beacon engine
		engine := New(ethash.NewFaker())
		chain := &mockChainReader{config: &config}

		// Create empty body
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  []*types.Withdrawal{},
		}

		// Call FinalizeAndAssemble
		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})

		if err != nil {
			t.Fatalf("FinalizeAndAssemble with larger state failed: %v", err)
		}

		if block == nil {
			t.Fatal("FinalizeAndAssemble returned nil block for block with larger state")
		}

		if receipts == nil {
			t.Fatal("FinalizeAndAssemble returned nil receipts for block with larger state")
		}

		// Commit time should be non-negative
		if commitTime < 0 {
			t.Fatalf("FinalizeAndAssemble returned negative commit time with larger state: %v", commitTime)
		}

		t.Logf("Commit time for PoS block with 100 storage entries: %v", commitTime)
	})

	t.Run("pre-merge block delegates to ethone", func(t *testing.T) {
		config := *params.TestChainConfig

		// Create state database
		db := rawdb.NewMemoryDatabase()
		tdb := triedb.NewDatabase(db, triedb.HashDefaults)
		statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))

		// Fund the test account
		statedb.SetBalance(address, uint256.MustFromBig(funds), tracing.BalanceChangeUnspecified)
		statedb.SetNonce(address, 0, tracing.NonceChangeUnspecified)

		// Create a pre-merge header (non-zero difficulty)
		header := &types.Header{
			Number:     big.NewInt(1),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000000,
			Difficulty: big.NewInt(1), // Non-zero difficulty indicates pre-merge
			Coinbase:   common.Address{},
			ParentHash: common.Hash{},
		}

		// Create beacon engine with ethash as the ethone engine
		engine := New(ethash.NewFaker())
		chain := &mockChainReader{config: &config}

		// Create empty body
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  []*types.Withdrawal{},
		}

		// Call FinalizeAndAssemble - should delegate to ethash
		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})

		// Verify no error occurred
		if err != nil {
			t.Fatalf("FinalizeAndAssemble failed for pre-merge block: %v", err)
		}

		// Verify block was created
		if block == nil {
			t.Fatal("FinalizeAndAssemble returned nil block for pre-merge")
		}

		// Verify receipts were returned
		if receipts == nil {
			t.Fatal("FinalizeAndAssemble returned nil receipts for pre-merge")
		}

		// Verify commit time is non-negative
		if commitTime < 0 {
			t.Fatalf("FinalizeAndAssemble returned negative commit time for pre-merge: %v", commitTime)
		}

		t.Logf("Commit time for pre-merge block (delegated to ethash): %v", commitTime)
	})

	t.Run("PoS block with withdrawals before Shanghai should fail", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		// Ensure Shanghai is not activated by setting it to a high block number
		config.ShanghaiBlock = big.NewInt(1000000)

		// Create state database
		db := rawdb.NewMemoryDatabase()
		tdb := triedb.NewDatabase(db, triedb.HashDefaults)
		statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))

		// Create a PoS header
		header := &types.Header{
			Number:     big.NewInt(1),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000000,
			Difficulty: common.Big0,
			Coinbase:   common.Address{},
			ParentHash: common.Hash{},
		}

		// Create beacon engine
		engine := New(ethash.NewFaker())
		chain := &mockChainReader{config: &config}

		// Create body with withdrawals before Shanghai activation
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  []*types.Withdrawal{{Validator: 1, Address: common.Address{0x01}, Amount: 100}},
		}

		// Call FinalizeAndAssemble - should return error
		_, _, _, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})

		// Verify error occurred
		if err == nil {
			t.Fatal("FinalizeAndAssemble should have failed with withdrawals before Shanghai")
		}

		if err.Error() != "withdrawals set before Shanghai activation" {
			t.Errorf("unexpected error: got %v, want 'withdrawals set before Shanghai activation'", err)
		}
	})

	t.Run("PoS block after Shanghai with nil withdrawals initializes empty slice", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		// Ensure Shanghai is activated
		config.ShanghaiBlock = big.NewInt(0)

		// Create state database
		db := rawdb.NewMemoryDatabase()
		tdb := triedb.NewDatabase(db, triedb.HashDefaults)
		statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))

		statedb.SetBalance(address, uint256.MustFromBig(funds), tracing.BalanceChangeUnspecified)
		statedb.SetNonce(address, 0, tracing.NonceChangeUnspecified)

		// Create a PoS header after Shanghai
		header := &types.Header{
			Number:     big.NewInt(1),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000000,
			Difficulty: common.Big0,
			Coinbase:   common.Address{},
			ParentHash: common.Hash{},
		}

		// Create beacon engine
		engine := New(ethash.NewFaker())
		chain := &mockChainReader{config: &config}

		// Create body with nil withdrawals (should be initialized to empty slice)
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  nil, // This should be initialized to empty slice
		}

		// Call FinalizeAndAssemble
		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})

		// Verify no error occurred
		if err != nil {
			t.Fatalf("FinalizeAndAssemble failed: %v", err)
		}

		// Verify block was created
		if block == nil {
			t.Fatal("FinalizeAndAssemble returned nil block")
		}

		// Verify receipts were returned
		if receipts == nil {
			t.Fatal("FinalizeAndAssemble returned nil receipts")
		}

		// Verify commit time is non-negative
		if commitTime < 0 {
			t.Fatalf("FinalizeAndAssemble returned negative commit time: %v", commitTime)
		}

		// Verify withdrawals were initialized to empty slice (not nil)
		if body.Withdrawals == nil {
			t.Error("Withdrawals should have been initialized to empty slice, got nil")
		}

		if len(body.Withdrawals) != 0 {
			t.Errorf("Withdrawals should be empty, got %d elements", len(body.Withdrawals))
		}

		t.Logf("Commit time for Shanghai PoS block with initialized withdrawals: %v", commitTime)
	})
}
