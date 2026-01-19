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
	"github.com/ethereum/go-ethereum/consensus/testutil"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

func TestFinalizeAndAssembleReturnsCommitTime(t *testing.T) {
	t.Parallel()

	t.Run("PoS block without withdrawals", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		_, _, _, statedb := testutil.SetupTestState(t)

		header := &types.Header{
			Number:     big.NewInt(1),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000000,
			Difficulty: common.Big0,
			Coinbase:   common.Address{},
			ParentHash: common.Hash{},
		}

		engine := New(ethash.NewFaker())
		chain := testutil.NewMockChainReader(&config)
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  []*types.Withdrawal{},
		}

		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})
		testutil.VerifyFinalizeAndAssembleSuccess(t, block, receipts, commitTime, err, header)
		t.Logf("Commit time for PoS block: %v", commitTime)
	})

	t.Run("PoS block with larger state", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		_, _, _, statedb := testutil.SetupTestState(t)

		// Add multiple storage entries to increase state size
		testAddr := common.Address{0xaa}
		statedb.SetBalance(testAddr, uint256.NewInt(100), tracing.BalanceChangeUnspecified)
		for i := 0; i < 100; i++ {
			key := common.BigToHash(big.NewInt(int64(i)))
			val := common.BigToHash(big.NewInt(int64(i * 2)))
			statedb.SetState(testAddr, key, val)
		}

		header := &types.Header{
			Number:     big.NewInt(2),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000001,
			Difficulty: common.Big0,
			Coinbase:   common.Address{},
			ParentHash: common.Hash{0x01},
		}

		engine := New(ethash.NewFaker())
		chain := testutil.NewMockChainReader(&config)
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  []*types.Withdrawal{},
		}

		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})
		testutil.VerifyFinalizeAndAssembleSuccess(t, block, receipts, commitTime, err, header)
		t.Logf("Commit time for PoS block with 100 storage entries: %v", commitTime)
	})

	t.Run("pre-merge block delegates to ethone", func(t *testing.T) {
		config := *params.TestChainConfig
		_, _, _, statedb := testutil.SetupTestState(t)

		header := &types.Header{
			Number:     big.NewInt(1),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000000,
			Difficulty: big.NewInt(1), // Non-zero difficulty indicates pre-merge
			Coinbase:   common.Address{},
			ParentHash: common.Hash{},
		}

		engine := New(ethash.NewFaker())
		chain := testutil.NewMockChainReader(&config)
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  []*types.Withdrawal{},
		}

		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})
		testutil.VerifyFinalizeAndAssembleSuccess(t, block, receipts, commitTime, err, header)
		t.Logf("Commit time for pre-merge block (delegated to ethash): %v", commitTime)
	})

	t.Run("PoS block with withdrawals before Shanghai should fail", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		config.ShanghaiBlock = big.NewInt(1000000)
		_, _, _, statedb := testutil.SetupTestState(t)

		header := &types.Header{
			Number:     big.NewInt(1),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000000,
			Difficulty: common.Big0,
			Coinbase:   common.Address{},
			ParentHash: common.Hash{},
		}

		engine := New(ethash.NewFaker())
		chain := testutil.NewMockChainReader(&config)
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  []*types.Withdrawal{{Validator: 1, Address: common.Address{0x01}, Amount: 100}},
		}

		_, _, _, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})

		if err == nil {
			t.Fatal("FinalizeAndAssemble should have failed with withdrawals before Shanghai")
		}
		if err.Error() != "withdrawals set before Shanghai activation" {
			t.Errorf("unexpected error: got %v, want 'withdrawals set before Shanghai activation'", err)
		}
	})

	t.Run("PoS block after Shanghai with nil withdrawals initializes empty slice", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		config.ShanghaiBlock = big.NewInt(0)
		_, _, _, statedb := testutil.SetupTestState(t)

		header := &types.Header{
			Number:     big.NewInt(1),
			GasLimit:   5000000,
			GasUsed:    0,
			Time:       1000000,
			Difficulty: common.Big0,
			Coinbase:   common.Address{},
			ParentHash: common.Hash{},
		}

		engine := New(ethash.NewFaker())
		chain := testutil.NewMockChainReader(&config)
		body := &types.Body{
			Transactions: []*types.Transaction{},
			Uncles:       []*types.Header{},
			Withdrawals:  nil, // This should be initialized to empty slice
		}

		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})
		testutil.VerifyFinalizeAndAssembleSuccess(t, block, receipts, commitTime, err, header)

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
