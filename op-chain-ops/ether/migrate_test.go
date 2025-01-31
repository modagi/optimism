package ether

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum-optimism/optimism/op-bindings/predeploys"

	"github.com/ethereum-optimism/optimism/op-chain-ops/crossdomain"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/stretchr/testify/require"
)

func TestMigrateBalances(t *testing.T) {
	tests := []struct {
		name            string
		totalSupply     *big.Int
		expDiff         *big.Int
		stateBalances   map[common.Address]*big.Int
		stateAllowances map[common.Address]common.Address
		inputAddresses  []common.Address
		inputAllowances []*crossdomain.Allowance
		check           func(t *testing.T, db *state.StateDB, err error)
	}{
		{
			name:        "everything matches",
			totalSupply: big.NewInt(3),
			expDiff:     big.NewInt(0),
			stateBalances: map[common.Address]*big.Int{
				common.HexToAddress("0x123"): big.NewInt(1),
				common.HexToAddress("0x456"): big.NewInt(2),
			},
			stateAllowances: map[common.Address]common.Address{
				common.HexToAddress("0x123"): common.HexToAddress("0x456"),
			},
			inputAddresses: []common.Address{
				common.HexToAddress("0x123"),
				common.HexToAddress("0x456"),
			},
			inputAllowances: []*crossdomain.Allowance{
				{
					From: common.HexToAddress("0x123"),
					To:   common.HexToAddress("0x456"),
				},
			},
			check: func(t *testing.T, db *state.StateDB, err error) {
				require.NoError(t, err)
				require.EqualValues(t, common.Big1, db.GetBalance(common.HexToAddress("0x123")))
				require.EqualValues(t, common.Big2, db.GetBalance(common.HexToAddress("0x456")))
				require.EqualValues(t, common.Hash{}, db.GetState(predeploys.LegacyERC20ETHAddr, GetOVMETHTotalSupplySlot()))
			},
		},
		{
			name:        "extra input addresses",
			totalSupply: big.NewInt(1),
			expDiff:     big.NewInt(0),
			stateBalances: map[common.Address]*big.Int{
				common.HexToAddress("0x123"): big.NewInt(1),
			},
			inputAddresses: []common.Address{
				common.HexToAddress("0x123"),
				common.HexToAddress("0x456"),
			},
			check: func(t *testing.T, db *state.StateDB, err error) {
				require.NoError(t, err)
				require.EqualValues(t, common.Big1, db.GetBalance(common.HexToAddress("0x123")))
				require.EqualValues(t, common.Big0, db.GetBalance(common.HexToAddress("0x456")))
				require.EqualValues(t, common.Hash{}, db.GetState(predeploys.LegacyERC20ETHAddr, GetOVMETHTotalSupplySlot()))
			},
		},
		{
			name:        "extra input allowances",
			totalSupply: big.NewInt(1),
			expDiff:     big.NewInt(0),
			stateBalances: map[common.Address]*big.Int{
				common.HexToAddress("0x123"): big.NewInt(1),
			},
			stateAllowances: map[common.Address]common.Address{
				common.HexToAddress("0x123"): common.HexToAddress("0x456"),
			},
			inputAddresses: []common.Address{
				common.HexToAddress("0x123"),
				common.HexToAddress("0x456"),
			},
			inputAllowances: []*crossdomain.Allowance{
				{
					From: common.HexToAddress("0x123"),
					To:   common.HexToAddress("0x456"),
				},
				{
					From: common.HexToAddress("0x123"),
					To:   common.HexToAddress("0x789"),
				},
			},
			check: func(t *testing.T, db *state.StateDB, err error) {
				require.NoError(t, err)
				require.EqualValues(t, common.Big1, db.GetBalance(common.HexToAddress("0x123")))
				require.EqualValues(t, common.Big0, db.GetBalance(common.HexToAddress("0x456")))
				require.EqualValues(t, common.Hash{}, db.GetState(predeploys.LegacyERC20ETHAddr, GetOVMETHTotalSupplySlot()))
			},
		},
		{
			name:        "missing input addresses",
			totalSupply: big.NewInt(2),
			expDiff:     big.NewInt(0),
			stateBalances: map[common.Address]*big.Int{
				common.HexToAddress("0x123"): big.NewInt(1),
				common.HexToAddress("0x456"): big.NewInt(1),
			},
			inputAddresses: []common.Address{
				common.HexToAddress("0x123"),
			},
			check: func(t *testing.T, db *state.StateDB, err error) {
				require.Error(t, err)
				require.ErrorContains(t, err, "unknown storage slot")
			},
		},
		{
			name:        "missing input allowances",
			totalSupply: big.NewInt(2),
			expDiff:     big.NewInt(0),
			stateBalances: map[common.Address]*big.Int{
				common.HexToAddress("0x123"): big.NewInt(1),
			},
			stateAllowances: map[common.Address]common.Address{
				common.HexToAddress("0x123"): common.HexToAddress("0x456"),
				common.HexToAddress("0x123"): common.HexToAddress("0x789"),
			},
			inputAddresses: []common.Address{
				common.HexToAddress("0x123"),
			},
			inputAllowances: []*crossdomain.Allowance{
				{
					From: common.HexToAddress("0x123"),
					To:   common.HexToAddress("0x456"),
				},
			},
			check: func(t *testing.T, db *state.StateDB, err error) {
				require.Error(t, err)
				require.ErrorContains(t, err, "unknown storage slot")
			},
		},
		{
			name:        "bad supply diff",
			totalSupply: big.NewInt(4),
			expDiff:     big.NewInt(0),
			stateBalances: map[common.Address]*big.Int{
				common.HexToAddress("0x123"): big.NewInt(1),
				common.HexToAddress("0x456"): big.NewInt(2),
			},
			inputAddresses: []common.Address{
				common.HexToAddress("0x123"),
				common.HexToAddress("0x456"),
			},
			check: func(t *testing.T, db *state.StateDB, err error) {
				require.Error(t, err)
				require.ErrorContains(t, err, "supply mismatch")
			},
		},
		{
			name:        "good supply diff",
			totalSupply: big.NewInt(4),
			expDiff:     big.NewInt(1),
			stateBalances: map[common.Address]*big.Int{
				common.HexToAddress("0x123"): big.NewInt(1),
				common.HexToAddress("0x456"): big.NewInt(2),
			},
			inputAddresses: []common.Address{
				common.HexToAddress("0x123"),
				common.HexToAddress("0x456"),
			},
			check: func(t *testing.T, db *state.StateDB, err error) {
				require.NoError(t, err)
				require.EqualValues(t, common.Big1, db.GetBalance(common.HexToAddress("0x123")))
				require.EqualValues(t, common.Big2, db.GetBalance(common.HexToAddress("0x456")))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, factory := makeLegacyETH(t, tt.totalSupply, tt.stateBalances, tt.stateAllowances)
			err := doMigration(db, factory, tt.inputAddresses, tt.inputAllowances, tt.expDiff, false)
			tt.check(t, db, err)
		})
	}
}

func makeLegacyETH(t *testing.T, totalSupply *big.Int, balances map[common.Address]*big.Int, allowances map[common.Address]common.Address) (*state.StateDB, DBFactory) {
	memDB := rawdb.NewMemoryDatabase()
	db, err := state.New(common.Hash{}, state.NewDatabaseWithConfig(memDB, &trie.Config{
		Preimages: true,
		Cache:     1024,
	}), nil)
	require.NoError(t, err)

	db.CreateAccount(OVMETHAddress)
	db.SetState(OVMETHAddress, getOVMETHTotalSupplySlot(), common.BigToHash(totalSupply))

	for slot := range ignoredSlots {
		if slot == getOVMETHTotalSupplySlot() {
			continue
		}
		db.SetState(OVMETHAddress, slot, common.Hash{31: 0xff})
	}
	for addr, balance := range balances {
		db.SetState(OVMETHAddress, CalcOVMETHStorageKey(addr), common.BigToHash(balance))
	}
	for from, to := range allowances {
		db.SetState(OVMETHAddress, CalcAllowanceStorageKey(from, to), common.BigToHash(big.NewInt(1)))
	}

	root, err := db.Commit(false)
	require.NoError(t, err)

	err = db.Database().TrieDB().Commit(root, true)
	require.NoError(t, err)

	return db, func() (*state.StateDB, error) {
		return state.New(root, state.NewDatabaseWithConfig(memDB, &trie.Config{
			Preimages: true,
			Cache:     1024,
		}), nil)
	}
}

// TestMigrateBalancesRandom tests that the pre-check balances function works
// with random addresses. This test makes sure that the partition logic doesn't
// miss anything.
func TestMigrateBalancesRandom(t *testing.T) {
	for i := 0; i < 100; i++ {
		addresses := make([]common.Address, 0)
		stateBalances := make(map[common.Address]*big.Int)

		allowances := make([]*crossdomain.Allowance, 0)
		stateAllowances := make(map[common.Address]common.Address)

		totalSupply := big.NewInt(0)

		for j := 0; j < rand.Intn(10000); j++ {
			addr := randAddr(t)
			addresses = append(addresses, addr)
			stateBalances[addr] = big.NewInt(int64(rand.Intn(1_000_000)))
			totalSupply = new(big.Int).Add(totalSupply, stateBalances[addr])
		}

		for j := 0; j < rand.Intn(1000); j++ {
			addr := randAddr(t)
			to := randAddr(t)
			allowances = append(allowances, &crossdomain.Allowance{
				From: addr,
				To:   to,
			})
			stateAllowances[addr] = to
		}

		db, factory := makeLegacyETH(t, totalSupply, stateBalances, stateAllowances)
		err := doMigration(db, factory, addresses, allowances, big.NewInt(0), false)
		require.NoError(t, err)

		for addr, expBal := range stateBalances {
			actBal := db.GetBalance(addr)
			require.EqualValues(t, expBal, actBal)
		}
	}
}

func TestPartitionKeyspace(t *testing.T) {
	tests := []struct {
		i        int
		count    int
		expected [2]common.Hash
	}{
		{
			i:     0,
			count: 1,
			expected: [2]common.Hash{
				common.HexToHash("0x00"),
				common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
			},
		},
		{
			i:     0,
			count: 2,
			expected: [2]common.Hash{
				common.HexToHash("0x00"),
				common.HexToHash("0x7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
			},
		},
		{
			i:     1,
			count: 2,
			expected: [2]common.Hash{
				common.HexToHash("0x7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
				common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
			},
		},
		{
			i:     0,
			count: 3,
			expected: [2]common.Hash{
				common.HexToHash("0x00"),
				common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555"),
			},
		},
		{
			i:     1,
			count: 3,
			expected: [2]common.Hash{
				common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555"),
				common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			},
		},
		{
			i:     2,
			count: 3,
			expected: [2]common.Hash{
				common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
				common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("i %d, count %d", tt.i, tt.count), func(t *testing.T) {
			start, end := PartitionKeyspace(tt.i, tt.count)
			require.Equal(t, tt.expected[0], start)
			require.Equal(t, tt.expected[1], end)
		})
	}

	t.Run("panics on invalid i or count", func(t *testing.T) {
		require.Panics(t, func() {
			PartitionKeyspace(1, 1)
		})
		require.Panics(t, func() {
			PartitionKeyspace(-1, 1)
		})
		require.Panics(t, func() {
			PartitionKeyspace(0, -1)
		})
		require.Panics(t, func() {
			PartitionKeyspace(-1, -1)
		})
	})
}

func randAddr(t *testing.T) common.Address {
	var addr common.Address
	_, err := rand.Read(addr[:])
	require.NoError(t, err)
	return addr
}
