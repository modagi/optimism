package ether

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"

	"github.com/ethereum-optimism/optimism/op-chain-ops/crossdomain"
	"github.com/ethereum-optimism/optimism/op-chain-ops/util"

	"github.com/ethereum-optimism/optimism/op-bindings/predeploys"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/log"
)

const (
	// checkJobs is the number of parallel workers to spawn
	// when iterating the storage trie.
	checkJobs = 64

	// BalanceSlot is an ordinal used to represent slots corresponding to OVM_ETH
	// balances in the state.
	BalanceSlot = 1

	// AllowanceSlot is an ordinal used to represent slots corresponding to OVM_ETH
	// allowances in the state.
	AllowanceSlot = 2
)

var (
	// OVMETHAddress is the address of the OVM ETH predeploy.
	OVMETHAddress = common.HexToAddress("0xDeadDeAddeAddEAddeadDEaDDEAdDeaDDeAD0000")

	ignoredSlots = map[common.Hash]bool{
		// Total Supply
		common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"): true,
		// Name
		common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000003"): true,
		// Symbol
		common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000004"): true,
		common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000005"): true,
		common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000006"): true,
	}

	// maxSlot is the maximum possible storage slot.
	maxSlot = common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	// sequencerEntrypointAddr is the address of the OVM sequencer entrypoint contract.
	sequencerEntrypointAddr = common.HexToAddress("0x4200000000000000000000000000000000000005")
)

// accountData is a wrapper struct that contains the balance and address of an account.
// It gets passed via channel to the collector process.
type accountData struct {
	balance    *big.Int
	legacySlot common.Hash
	address    common.Address
}

type DBFactory func() (*state.StateDB, error)

// MigrateBalances migrates all balances in the LegacyERC20ETH contract into state. It performs checks
// in parallel with mutations in order to reduce overall migration time.
func MigrateBalances(mutableDB *state.StateDB, dbFactory DBFactory, addresses []common.Address, allowances []*crossdomain.Allowance, chainID int, noCheck bool) error {
	// Chain params to use for integrity checking.
	params := crossdomain.ParamsByChainID[chainID]
	if params == nil {
		return fmt.Errorf("no chain params for %d", chainID)
	}

	return doMigration(mutableDB, dbFactory, addresses, allowances, params.ExpectedSupplyDelta, noCheck)
}

func doMigration(mutableDB *state.StateDB, dbFactory DBFactory, addresses []common.Address, allowances []*crossdomain.Allowance, expDiff *big.Int, noCheck bool) error {
	// We'll need to maintain a list of all addresses that we've seen along with all of the storage
	// slots based on the witness data.
	slotsAddrs := make(map[common.Hash]common.Address)
	slotsInp := make(map[common.Hash]int)

	// For each known address, compute its balance key and add it to the list of addresses.
	// Mint events are instrumented as regular ETH events in the witness data, so we no longer
	// need to iterate over mint events during the migration.
	for _, addr := range addresses {
		sk := CalcOVMETHStorageKey(addr)
		slotsAddrs[sk] = addr
		slotsInp[sk] = BalanceSlot
	}

	// For each known allowance, compute its storage key and add it to the list of addresses.
	for _, allowance := range allowances {
		sk := CalcAllowanceStorageKey(allowance.From, allowance.To)
		slotsAddrs[sk] = allowance.From
		slotsInp[sk] = AllowanceSlot
	}

	// Add the old SequencerEntrypoint because someone sent it ETH a long time ago and it has a
	// balance but none of our instrumentation could easily find it. Special case.
	entrySK := CalcOVMETHStorageKey(sequencerEntrypointAddr)
	slotsAddrs[entrySK] = sequencerEntrypointAddr
	slotsInp[entrySK] = BalanceSlot

	// WaitGroup to wait on each iteration job to finish.
	var wg sync.WaitGroup
	// Channel to receive storage slot keys and values from each iteration job.
	outCh := make(chan accountData)
	// Channel to receive errors from each iteration job.
	errCh := make(chan error, checkJobs)
	// Channel to cancel all iteration jobs as well as the collector.
	cancelCh := make(chan struct{})

	// Define a worker function to iterate over each partition.
	worker := func(start, end common.Hash) {
		// Decrement the WaitGroup when the function returns.
		defer wg.Done()

		db, err := dbFactory()
		if err != nil {
			log.Crit("cannot get database", "err", err)
		}

		// Create a new storage trie. Each trie returned by db.StorageTrie
		// is a copy, so this is safe for concurrent use.
		st, err := db.StorageTrie(predeploys.LegacyERC20ETHAddr)
		if err != nil {
			// Should never happen, so explode if it does.
			log.Crit("cannot get storage trie for LegacyERC20ETHAddr", "err", err)
		}
		if st == nil {
			// Should never happen, so explode if it does.
			log.Crit("nil storage trie for LegacyERC20ETHAddr")
		}

		it := trie.NewIterator(st.NodeIterator(start.Bytes()))

		// Below code is largely based on db.ForEachStorage. We can't use that
		// because it doesn't allow us to specify a start and end key.
		for it.Next() {
			select {
			case <-cancelCh:
				// If one of the workers encounters an error, cancel all of them.
				return
			default:
				break
			}

			// Use the raw (i.e., secure hashed) key to check if we've reached
			// the end of the partition. Use > rather than >= here to account for
			// the fact that the values returned by PartitionKeys are inclusive.
			// Duplicate addresses that may be returned by this iteration are
			// filtered out in the collector.
			if new(big.Int).SetBytes(it.Key).Cmp(end.Big()) > 0 {
				return
			}

			// Skip if the value is empty.
			rawValue := it.Value
			if len(rawValue) == 0 {
				continue
			}

			// Get the preimage.
			rawKey := st.GetKey(it.Key)
			if rawKey == nil {
				// Should never happen, so explode if it does.
				log.Crit("cannot get preimage for storage key", "key", it.Key)
			}
			key := common.BytesToHash(rawKey)

			// Parse the raw value.
			_, content, _, err := rlp.Split(rawValue)
			if err != nil {
				// Should never happen, so explode if it does.
				log.Crit("mal-formed data in state: %v", err)
			}

			// We can safely ignore specific slots (totalSupply, name, symbol).
			if ignoredSlots[key] {
				continue
			}

			slotType, ok := slotsInp[key]
			if !ok {
				if noCheck {
					log.Error("ignoring unknown storage slot in state", "slot", key.String())
				} else {
					errCh <- fmt.Errorf("unknown storage slot in state: %s", key.String())
					return
				}
			}

			// No accounts should have a balance in state. If they do, bail.
			addr, ok := slotsAddrs[key]
			if !ok {
				log.Crit("could not find address in map - should never happen")
			}
			bal := db.GetBalance(addr)
			if bal.Sign() != 0 {
				log.Error(
					"account has non-zero balance in state - should never happen",
					"addr", addr,
					"balance", bal.String(),
				)
				if !noCheck {
					errCh <- fmt.Errorf("account has non-zero balance in state - should never happen: %s", addr.String())
					return
				}
			}

			// Add balances to the total found.
			switch slotType {
			case BalanceSlot:
				// Convert the value to a common.Hash, then send to the channel.
				value := common.BytesToHash(content)
				outCh <- accountData{
					balance:    value.Big(),
					legacySlot: key,
					address:    addr,
				}
			case AllowanceSlot:
				// Allowance slot.
				continue
			default:
				// Should never happen.
				if noCheck {
					log.Error("unknown slot type", "slot", key, "type", slotType)
				} else {
					log.Crit("unknown slot type %d, should never happen", slotType)
				}
			}
		}
	}

	for i := 0; i < checkJobs; i++ {
		wg.Add(1)

		// Partition the keyspace per worker.
		start, end := PartitionKeyspace(i, checkJobs)

		// Kick off our worker.
		go worker(start, end)
	}

	// Make a channel to make sure that the collector process completes.
	collectorCloseCh := make(chan struct{})

	// Keep track of the last error seen.
	var lastErr error

	// There are multiple ways that the cancel channel can be closed:
	// - if we receive an error from the errCh
	// - if the collector process completes
	// To prevent panics, we wrap the close in a sync.Once.
	var cancelOnce sync.Once

	// Create a map of accounts we've seen so that we can filter out duplicates.
	seenAccounts := make(map[common.Address]bool)

	// Keep track of the total migrated supply.
	totalFound := new(big.Int)

	// Kick off another background process to collect
	// values from the channel and add them to the map.
	var count int
	progress := util.ProgressLogger(1000, "Migrated OVM_ETH storage slot")
	go func() {
		defer func() {
			collectorCloseCh <- struct{}{}
		}()
		for {
			select {
			case account := <-outCh:
				progress()

				// Filter out duplicate accounts. See the below note about keyspace iteration for
				// why we may have to filter out duplicates.
				if seenAccounts[account.address] {
					log.Info("skipping duplicate account during iteration", "addr", account.address)
					continue
				}

				// Accumulate addresses and total supply.
				totalFound = new(big.Int).Add(totalFound, account.balance)

				mutableDB.SetBalance(account.address, account.balance)
				mutableDB.SetState(predeploys.LegacyERC20ETHAddr, account.legacySlot, common.Hash{})
				count++
				seenAccounts[account.address] = true
			case err := <-errCh:
				cancelOnce.Do(func() {
					lastErr = err
					close(cancelCh)
				})
			case <-cancelCh:
				return
			}
		}
	}()

	// Wait for the workers to finish.
	wg.Wait()
	// Close the cancel channel to signal the collector process to stop.
	cancelOnce.Do(func() {
		close(cancelCh)
	})

	// Wait for the collector process to finish.
	<-collectorCloseCh

	// If we saw an error, return it.
	if lastErr != nil {
		return lastErr
	}

	// Log how many slots were iterated over.
	log.Info("Iterated legacy balances", "count", count)

	// Verify the supply delta. Recorded total supply in the LegacyERC20ETH contract may be higher
	// than the actual migrated amount because self-destructs will remove ETH supply in a way that
	// cannot be reflected in the contract. This is fine because self-destructs just mean the L2 is
	// actually *overcollateralized* by some tiny amount.
	db, err := dbFactory()
	if err != nil {
		log.Crit("cannot get database", "err", err)
	}

	totalSupply := getOVMETHTotalSupply(db)
	delta := new(big.Int).Sub(totalSupply, totalFound)
	if delta.Cmp(expDiff) != 0 {
		log.Error(
			"supply mismatch",
			"migrated", totalFound.String(),
			"supply", totalSupply.String(),
			"delta", delta.String(),
			"exp_delta", expDiff.String(),
		)
		if !noCheck {
			return fmt.Errorf("supply mismatch: %s", delta.String())
		}
	}

	// Supply is verified.
	log.Info(
		"supply verified OK",
		"migrated", totalFound.String(),
		"supply", totalSupply.String(),
		"delta", delta.String(),
		"exp_delta", expDiff.String(),
	)

	// Set the total supply to 0. We do this because the total supply is necessarily going to be
	// different than the sum of all balances since we no longer track balances inside the contract
	// itself. The total supply is going to be weird no matter what, might as well set it to zero
	// so it's explicitly weird instead of implicitly weird.
	mutableDB.SetState(predeploys.LegacyERC20ETHAddr, getOVMETHTotalSupplySlot(), common.Hash{})
	log.Info("Set the totalSupply to 0")

	return nil
}

// PartitionKeyspace divides the key space into partitions by dividing the maximum keyspace
// by count then multiplying by i. This will leave some slots left over, which we handle below. It
// returns the start and end keys for the partition as a common.Hash. Note that the returned range
// of keys is inclusive, i.e., [start, end] NOT [start, end).
func PartitionKeyspace(i int, count int) (common.Hash, common.Hash) {
	if i < 0 || count < 0 {
		panic("i and count must be greater than 0")
	}

	if i > count-1 {
		panic("i must be less than count - 1")
	}

	// Divide the key space into partitions by dividing the key space by the number
	// of jobs. This will leave some slots left over, which we handle below.
	partSize := new(big.Int).Div(maxSlot.Big(), big.NewInt(int64(count)))

	start := common.BigToHash(new(big.Int).Mul(big.NewInt(int64(i)), partSize))
	var end common.Hash
	if i < count-1 {
		// If this is not the last partition, use the next partition's start key as the end.
		end = common.BigToHash(new(big.Int).Mul(big.NewInt(int64(i+1)), partSize))
	} else {
		// If this is the last partition, use the max slot as the end.
		end = maxSlot
	}

	return start, end
}
