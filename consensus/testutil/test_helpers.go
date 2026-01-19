// Copyright 2024 The go-ethereum Authors
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

package testutil

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// SetupTestState creates a test state database and funds the test account
func SetupTestState(t *testing.T) (common.Address, *big.Int, *triedb.Database, *state.StateDB) {
	key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	address := crypto.PubkeyToAddress(key.PublicKey)
	funds := big.NewInt(0).Mul(big.NewInt(1000), big.NewInt(params.Ether))

	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))

	statedb.SetBalance(address, uint256.MustFromBig(funds), tracing.BalanceChangeUnspecified)
	statedb.SetNonce(address, 0, tracing.NonceChangeUnspecified)

	return address, funds, tdb, statedb
}

// VerifyFinalizeAndAssembleSuccess checks common success conditions for FinalizeAndAssemble
func VerifyFinalizeAndAssembleSuccess(t *testing.T, block *types.Block, receipts []*types.Receipt, commitTime time.Duration, err error, header *types.Header) {
	if err != nil {
		t.Fatalf("FinalizeAndAssemble failed: %v", err)
	}
	if block == nil {
		t.Fatal("FinalizeAndAssemble returned nil block")
	}
	if receipts == nil {
		t.Fatal("FinalizeAndAssemble returned nil receipts")
	}
	if commitTime < 0 {
		t.Fatalf("FinalizeAndAssemble returned negative commit time: %v", commitTime)
	}
	if block.Number().Cmp(header.Number) != 0 {
		t.Errorf("Block number mismatch: got %v, want %v", block.Number(), header.Number)
	}
	if block.GasLimit() != header.GasLimit {
		t.Errorf("Block gas limit mismatch: got %v, want %v", block.GasLimit(), header.GasLimit)
	}
	if block.Root() == (common.Hash{}) {
		t.Error("Block root hash is empty")
	}
	if header.Root == (common.Hash{}) {
		t.Error("Header root hash is empty")
	}
}
