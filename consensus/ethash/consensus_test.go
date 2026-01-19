// Copyright 2017 The go-ethereum Authors
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

package ethash

import (
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

type diffTest struct {
	ParentTimestamp    uint64
	ParentDifficulty   *big.Int
	CurrentTimestamp   uint64
	CurrentBlocknumber *big.Int
	CurrentDifficulty  *big.Int
}

func (d *diffTest) UnmarshalJSON(b []byte) (err error) {
	var ext struct {
		ParentTimestamp    string
		ParentDifficulty   string
		CurrentTimestamp   string
		CurrentBlocknumber string
		CurrentDifficulty  string
	}

	if err := json.Unmarshal(b, &ext); err != nil {
		return err
	}

	d.ParentTimestamp = math.MustParseUint64(ext.ParentTimestamp)
	d.ParentDifficulty = math.MustParseBig256(ext.ParentDifficulty)
	d.CurrentTimestamp = math.MustParseUint64(ext.CurrentTimestamp)
	d.CurrentBlocknumber = math.MustParseBig256(ext.CurrentBlocknumber)
	d.CurrentDifficulty = math.MustParseBig256(ext.CurrentDifficulty)

	return nil
}

func TestCalcDifficulty(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "tests", "testdata", "BasicTests", "difficulty.json"))
	if err != nil {
		t.Skip(err)
	}
	defer file.Close()

	tests := make(map[string]diffTest)

	err = json.NewDecoder(file).Decode(&tests)
	if err != nil {
		t.Fatal(err)
	}

	config := &params.ChainConfig{HomesteadBlock: big.NewInt(1150000)}

	for name, test := range tests {
		number := new(big.Int).Sub(test.CurrentBlocknumber, big.NewInt(1))

		diff := CalcDifficulty(config, test.CurrentTimestamp, &types.Header{
			Number:     number,
			Time:       test.ParentTimestamp,
			Difficulty: test.ParentDifficulty,
		})
		if diff.Cmp(test.CurrentDifficulty) != 0 {
			t.Error(name, "failed. Expected", test.CurrentDifficulty, "and calculated", diff)
		}
	}
}

func randSlice(min, max uint32) []byte {
	var b = make([]byte, 4)
	_, _ = crand.Read(b)
	a := binary.LittleEndian.Uint32(b)
	size := min + a%(max-min)
	out := make([]byte, size)
	_, _ = crand.Read(out)

	return out
}

func TestDifficultyCalculators(t *testing.T) {
	for i := 0; i < 5000; i++ {
		// 1 to 300 seconds diff
		var timeDelta = uint64(1 + rand.Uint32()%3000)

		diffBig := new(big.Int).SetBytes(randSlice(2, 10))
		if diffBig.Cmp(params.MinimumDifficulty) < 0 {
			diffBig.Set(params.MinimumDifficulty)
		}
		//rand.Read(difficulty)
		header := &types.Header{
			Difficulty: diffBig,
			Number:     new(big.Int).SetUint64(rand.Uint64() % 50_000_000),
			Time:       rand.Uint64() - timeDelta,
		}
		if rand.Uint32()&1 == 0 {
			header.UncleHash = types.EmptyUncleHash
		}

		bombDelay := new(big.Int).SetUint64(rand.Uint64() % 50_000_000)
		for i, pair := range []struct {
			bigFn  func(time uint64, parent *types.Header) *big.Int
			u256Fn func(time uint64, parent *types.Header) *big.Int
		}{
			{FrontierDifficultyCalculator, CalcDifficultyFrontierU256},
			{HomesteadDifficultyCalculator, CalcDifficultyHomesteadU256},
			{DynamicDifficultyCalculator(bombDelay), MakeDifficultyCalculatorU256(bombDelay)},
		} {
			time := header.Time + timeDelta
			want := pair.bigFn(time, header)
			have := pair.u256Fn(time, header)

			if want.BitLen() > 256 {
				continue
			}

			if want.Cmp(have) != 0 {
				t.Fatalf("pair %d: want %x have %x\nparent.Number: %x\np.Time: %x\nc.Time: %x\nBombdelay: %v\n", i, want, have,
					header.Number, header.Time, time, bombDelay)
			}
		}
	}
}

func BenchmarkDifficultyCalculator(b *testing.B) {
	x1 := makeDifficultyCalculator(big.NewInt(1000000))
	x2 := MakeDifficultyCalculatorU256(big.NewInt(1000000))
	h := &types.Header{
		ParentHash: common.Hash{},
		UncleHash:  types.EmptyUncleHash,
		Difficulty: big.NewInt(0xffffff),
		Number:     big.NewInt(500000),
		Time:       1000000,
	}

	b.Run("big-frontier", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			calcDifficultyFrontier(1000014, h)
		}
	})
	b.Run("u256-frontier", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			CalcDifficultyFrontierU256(1000014, h)
		}
	})
	b.Run("big-homestead", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			calcDifficultyHomestead(1000014, h)
		}
	})
	b.Run("u256-homestead", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			CalcDifficultyHomesteadU256(1000014, h)
		}
	})
	b.Run("big-generic", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			x1(1000014, h)
		}
	})
	b.Run("u256-generic", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			x2(1000014, h)
		}
	})
}

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
		config  = params.TestChainConfig
	)

	// Create state database
	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))

	// Fund the test account
	statedb.SetBalance(address, uint256.MustFromBig(funds), tracing.BalanceChangeUnspecified)
	statedb.SetNonce(address, 0, tracing.NonceChangeUnspecified)

	// Create a header for the new block
	header := &types.Header{
		Number:     big.NewInt(1),
		GasLimit:   5000000,
		GasUsed:    0,
		Time:       1000000,
		Difficulty: big.NewInt(1),
		Coinbase:   common.Address{},
		ParentHash: common.Hash{},
	}

	// Create ethash engine and chain reader
	engine := NewFaker()
	chain := &mockChainReader{config: config}

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

	// Test with a transaction to verify commit time is measured with more state changes
	statedb2, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))
	statedb2.SetBalance(address, uint256.MustFromBig(funds), tracing.BalanceChangeUnspecified)
	statedb2.SetNonce(address, 0, tracing.NonceChangeUnspecified)

	// Add multiple storage entries to increase state size
	testAddr := common.Address{0xaa}
	statedb2.SetBalance(testAddr, uint256.NewInt(100), tracing.BalanceChangeUnspecified)
	for i := 0; i < 100; i++ {
		key := common.BigToHash(big.NewInt(int64(i)))
		val := common.BigToHash(big.NewInt(int64(i * 2)))
		statedb2.SetState(testAddr, key, val)
	}

	header2 := &types.Header{
		Number:     big.NewInt(2),
		GasLimit:   5000000,
		GasUsed:    0,
		Time:       1000001,
		Difficulty: big.NewInt(1),
		Coinbase:   common.Address{},
		ParentHash: block.Hash(),
	}

	body2 := &types.Body{
		Transactions: []*types.Transaction{},
		Uncles:       []*types.Header{},
		Withdrawals:  []*types.Withdrawal{},
	}

	block2, receipts2, commitTime2, err2 := engine.FinalizeAndAssemble(chain, header2, statedb2, body2, []*types.Receipt{})

	if err2 != nil {
		t.Fatalf("FinalizeAndAssemble with larger state failed: %v", err2)
	}

	if block2 == nil {
		t.Fatal("FinalizeAndAssemble returned nil block for block with larger state")
	}

	if receipts2 == nil {
		t.Fatal("FinalizeAndAssemble returned nil receipts for block with larger state")
	}

	// Commit time should be non-negative
	if commitTime2 < 0 {
		t.Fatalf("FinalizeAndAssemble returned negative commit time with larger state: %v", commitTime2)
	}

	// With more state changes, commit time should typically be positive (though not guaranteed)
	// We just verify it's measured correctly
	t.Logf("Commit time for empty state: %v", commitTime)
	t.Logf("Commit time for state with 100 storage entries: %v", commitTime2)
}
