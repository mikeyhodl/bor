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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// MockChainReader is a minimal implementation of consensus.ChainHeaderReader for testing
type MockChainReader struct {
	config *params.ChainConfig
}

// NewMockChainReader creates a new mock chain reader with the given configuration
func NewMockChainReader(config *params.ChainConfig) consensus.ChainHeaderReader {
	return &MockChainReader{config: config}
}

func (m *MockChainReader) Config() *params.ChainConfig {
	return m.config
}

func (m *MockChainReader) CurrentHeader() *types.Header {
	return &types.Header{}
}

func (m *MockChainReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	return nil
}

func (m *MockChainReader) GetHeaderByNumber(number uint64) *types.Header {
	return nil
}

func (m *MockChainReader) GetHeaderByHash(hash common.Hash) *types.Header {
	return nil
}

func (m *MockChainReader) GetTd(hash common.Hash, number uint64) *big.Int {
	return big.NewInt(0)
}
