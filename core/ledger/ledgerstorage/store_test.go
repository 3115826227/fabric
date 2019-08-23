/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package ledgerstorage

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang/protobuf/proto"
	"github.com/hyperledger/fabric/common/flogging"
	"github.com/hyperledger/fabric/common/ledger/blkstorage"
	"github.com/hyperledger/fabric/common/ledger/blkstorage/fsblkstorage"
	"github.com/hyperledger/fabric/common/ledger/testutil"
	"github.com/hyperledger/fabric/common/metrics/disabled"
	"github.com/hyperledger/fabric/core/ledger"
	"github.com/hyperledger/fabric/core/ledger/pvtdatapolicy"
	btltestutil "github.com/hyperledger/fabric/core/ledger/pvtdatapolicy/testutil"
	"github.com/hyperledger/fabric/core/ledger/pvtdatastorage"
	lutil "github.com/hyperledger/fabric/core/ledger/util"
	"github.com/hyperledger/fabric/protos/common"
	"github.com/hyperledger/fabric/protos/ledger/rwset"
	pb "github.com/hyperledger/fabric/protos/peer"
	"github.com/stretchr/testify/assert"
)

var metricsProvider = &disabled.Provider{}

func TestMain(m *testing.M) {
	flogging.ActivateSpec("ledgerstorage,pvtdatastorage=debug")
	os.Exit(m.Run())
}

func TestStore(t *testing.T) {
	storeDir, err := ioutil.TempDir("", "lstore")
	if err != nil {
		t.Fatalf("Failed to create ledger storage directory: %s", err)
	}
	defer os.RemoveAll(storeDir)
	conf := buildPrivateDataConfig(storeDir)
	blockStoreDir := filepath.Join(storeDir, "chains")
	provider := NewProvider(blockStoreDir, conf, metricsProvider)
	defer provider.Close()
	store, err := provider.Open("testLedger")
	store.Init(btlPolicyForSampleData())
	defer store.Shutdown()

	assert.NoError(t, err)
	sampleData := sampleDataWithPvtdataForSelectiveTx(t)
	for _, sampleDatum := range sampleData {
		assert.NoError(t, store.CommitWithPvtData(sampleDatum))
	}

	// block 1 has no pvt data
	pvtdata, err := store.GetPvtDataByNum(1, nil)
	assert.NoError(t, err)
	assert.Nil(t, pvtdata)

	// block 4 has no pvt data
	pvtdata, err = store.GetPvtDataByNum(4, nil)
	assert.NoError(t, err)
	assert.Nil(t, pvtdata)

	// block 2 has pvt data for tx 3, 5 and 6. Though the tx 6
	// is marked as invalid in the block, the pvtData should
	// have been stored
	pvtdata, err = store.GetPvtDataByNum(2, nil)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(pvtdata))
	assert.Equal(t, uint64(3), pvtdata[0].SeqInBlock)
	assert.Equal(t, uint64(5), pvtdata[1].SeqInBlock)
	assert.Equal(t, uint64(6), pvtdata[2].SeqInBlock)

	// block 3 has pvt data for tx 4 and 6 only
	pvtdata, err = store.GetPvtDataByNum(3, nil)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(pvtdata))
	assert.Equal(t, uint64(4), pvtdata[0].SeqInBlock)
	assert.Equal(t, uint64(6), pvtdata[1].SeqInBlock)

	blockAndPvtdata, err := store.GetPvtDataAndBlockByNum(2, nil)
	assert.NoError(t, err)
	assert.True(t, proto.Equal(sampleData[2].Block, blockAndPvtdata.Block))

	blockAndPvtdata, err = store.GetPvtDataAndBlockByNum(3, nil)
	assert.NoError(t, err)
	assert.True(t, proto.Equal(sampleData[3].Block, blockAndPvtdata.Block))

	// pvt data retrieval for block 3 with filter should return filtered pvtdata
	filter := ledger.NewPvtNsCollFilter()
	filter.Add("ns-1", "coll-1")
	blockAndPvtdata, err = store.GetPvtDataAndBlockByNum(3, filter)
	assert.NoError(t, err)
	assert.Equal(t, sampleData[3].Block, blockAndPvtdata.Block)
	// two transactions should be present
	assert.Equal(t, 2, len(blockAndPvtdata.PvtData))
	// both tran number 4 and 6 should have only one collection because of filter
	assert.Equal(t, 1, len(blockAndPvtdata.PvtData[4].WriteSet.NsPvtRwset))
	assert.Equal(t, 1, len(blockAndPvtdata.PvtData[6].WriteSet.NsPvtRwset))
	// any other transaction entry should be nil
	assert.Nil(t, blockAndPvtdata.PvtData[2])

	// test missing data retrieval in the presence of invalid tx. Block 5 had
	// missing data (for tx4 and tx5). Though tx5 was marked as invalid tx,
	// both tx4 and tx5 missing data should be returned
	expectedMissingDataInfo := make(ledger.MissingPvtDataInfo)
	expectedMissingDataInfo.Add(5, 4, "ns-4", "coll-4")
	expectedMissingDataInfo.Add(5, 5, "ns-5", "coll-5")
	missingDataInfo, err := store.GetMissingPvtDataInfoForMostRecentBlocks(1)
	assert.NoError(t, err)
	assert.Equal(t, expectedMissingDataInfo, missingDataInfo)
}

func TestStoreWithExistingBlockchain(t *testing.T) {
	testLedgerid := "test-ledger"
	storeDir, err := ioutil.TempDir("", "lstore")
	if err != nil {
		t.Fatalf("Failed to create ledger storage directory: %s", err)
	}
	defer os.RemoveAll(storeDir)

	// Construct a block storage
	attrsToIndex := []blkstorage.IndexableAttr{
		blkstorage.IndexableAttrBlockHash,
		blkstorage.IndexableAttrBlockNum,
		blkstorage.IndexableAttrTxID,
		blkstorage.IndexableAttrBlockNumTranNum,
		blkstorage.IndexableAttrBlockTxID,
		blkstorage.IndexableAttrTxValidationCode,
	}
	indexConfig := &blkstorage.IndexConfig{AttrsToIndex: attrsToIndex}
	blockStoreProvider := fsblkstorage.NewProvider(
		fsblkstorage.NewConf(filepath.Join(storeDir, "chains"), maxBlockFileSize),
		indexConfig,
		metricsProvider,
	)

	blkStore, err := blockStoreProvider.OpenBlockStore(testLedgerid)
	assert.NoError(t, err)
	testBlocks := testutil.ConstructTestBlocks(t, 10)

	existingBlocks := testBlocks[0:9]
	blockToAdd := testBlocks[9:][0]

	// Add existingBlocks to the block storage directly without involving pvtdata store and close the block storage
	for _, blk := range existingBlocks {
		assert.NoError(t, blkStore.AddBlock(blk))
	}
	blockStoreProvider.Close()

	// Simulating the upgrade from 1.0 situation:
	// Open the ledger storage - pvtdata store is opened for the first time with an existing block storage
	conf := buildPrivateDataConfig(storeDir)
	blockStoreDir := filepath.Join(storeDir, "chains")
	provider := NewProvider(blockStoreDir, conf, metricsProvider)
	defer provider.Close()
	store, err := provider.Open(testLedgerid)
	store.Init(btlPolicyForSampleData())
	defer store.Shutdown()

	// test that pvtdata store is updated with info from existing block storage
	pvtdataBlockHt, err := store.pvtdataStore.LastCommittedBlockHeight()
	assert.NoError(t, err)
	assert.Equal(t, uint64(9), pvtdataBlockHt)

	// Add one more block with ovtdata associated with one of the trans and commit in the normal course
	pvtdata := samplePvtData(t, []uint64{0})
	assert.NoError(t, store.CommitWithPvtData(&ledger.BlockAndPvtData{Block: blockToAdd, PvtData: pvtdata}))
	pvtdataBlockHt, err = store.pvtdataStore.LastCommittedBlockHeight()
	assert.NoError(t, err)
	assert.Equal(t, uint64(10), pvtdataBlockHt)
}

func TestCrashAfterPvtdataStoreCommit(t *testing.T) {
	storeDir, err := ioutil.TempDir("", "lstore")
	if err != nil {
		t.Fatalf("Failed to create ledger storage directory: %s", err)
	}
	defer os.RemoveAll(storeDir)
	conf := buildPrivateDataConfig(storeDir)
	blockStoreDir := filepath.Join(storeDir, "chains")
	provider := NewProvider(blockStoreDir, conf, metricsProvider)
	defer provider.Close()
	store, err := provider.Open("testLedger")
	store.Init(btlPolicyForSampleData())
	defer store.Shutdown()
	assert.NoError(t, err)

	sampleData := sampleDataWithPvtdataForAllTxs(t)
	dataBeforeCrash := sampleData[0:3]
	dataAtCrash := sampleData[3]

	for _, sampleDatum := range dataBeforeCrash {
		assert.NoError(t, store.CommitWithPvtData(sampleDatum))
	}
	blockNumAtCrash := dataAtCrash.Block.Header.Number
	var pvtdataAtCrash []*ledger.TxPvtData
	for _, p := range dataAtCrash.PvtData {
		pvtdataAtCrash = append(pvtdataAtCrash, p)
	}
	// call Commit on pvt data store and mimic a crash before committing the block to block store
	store.pvtdataStore.Commit(blockNumAtCrash, pvtdataAtCrash, nil)
	store.Shutdown()
	provider.Close()
	provider = NewProvider(blockStoreDir, conf, metricsProvider)
	store, err = provider.Open("testLedger")
	assert.NoError(t, err)
	store.Init(btlPolicyForSampleData())

	// When starting the storage after a crash, we should be able to fetch the pvtData from pvtStore
	testVerifyPvtData(t, store, blockNumAtCrash, dataAtCrash.PvtData)
	bcInfo, err := store.GetBlockchainInfo()
	assert.NoError(t, err)
	assert.Equal(t, blockNumAtCrash, bcInfo.Height)

	// we should be able to write the last block again
	// to ensure that the pvtdataStore is not updated, we send a different pvtData for
	// the same block such that we can retrieve the pvtData and compare.
	expectedPvtData := dataAtCrash.PvtData
	dataAtCrash.PvtData = make(ledger.TxPvtDataMap)
	dataAtCrash.PvtData[0] = &ledger.TxPvtData{
		SeqInBlock: 0,
		WriteSet: &rwset.TxPvtReadWriteSet{
			NsPvtRwset: []*rwset.NsPvtReadWriteSet{
				{
					Namespace: "ns-1",
					CollectionPvtRwset: []*rwset.CollectionPvtReadWriteSet{
						{
							CollectionName: "coll-1",
							Rwset:          []byte("pvtdata"),
						},
					},
				},
			},
		},
	}
	assert.NoError(t, store.CommitWithPvtData(dataAtCrash))
	testVerifyPvtData(t, store, blockNumAtCrash, expectedPvtData)
	bcInfo, err = store.GetBlockchainInfo()
	assert.NoError(t, err)
	assert.Equal(t, blockNumAtCrash+1, bcInfo.Height)

}

func testVerifyPvtData(t *testing.T, store *Store, blockNum uint64, expectedPvtData ledger.TxPvtDataMap) {
	pvtdata, err := store.GetPvtDataByNum(blockNum, nil)
	assert.NoError(t, err)
	constructed := constructPvtdataMap(pvtdata)
	assert.Equal(t, len(expectedPvtData), len(constructed))
	for k, v := range expectedPvtData {
		ov, ok := constructed[k]
		assert.True(t, ok)
		assert.Equal(t, v.SeqInBlock, ov.SeqInBlock)
		assert.True(t, proto.Equal(v.WriteSet, ov.WriteSet))
	}
}

func TestAddAfterPvtdataStoreError(t *testing.T) {
	storeDir, err := ioutil.TempDir("", "lstore")
	if err != nil {
		t.Fatalf("Failed to create ledger storage directory: %s", err)
	}
	defer os.RemoveAll(storeDir)
	conf := buildPrivateDataConfig(storeDir)
	blockStoreDir := filepath.Join(storeDir, "chains")
	provider := NewProvider(blockStoreDir, conf, metricsProvider)
	defer provider.Close()
	store, err := provider.Open("testLedger")
	store.Init(btlPolicyForSampleData())
	defer store.Shutdown()
	assert.NoError(t, err)

	sampleData := sampleDataWithPvtdataForAllTxs(t)
	for _, d := range sampleData[0:9] {
		assert.NoError(t, store.CommitWithPvtData(d))
	}
	// try to write the last block again. The function should skip adding block to the private store
	// as the pvt store but the block storage should return error
	assert.Error(t, store.CommitWithPvtData(sampleData[8]))

	// At the end, the pvt store status should not have changed
	pvtStoreCommitHt, err := store.pvtdataStore.LastCommittedBlockHeight()
	assert.NoError(t, err)
	assert.Equal(t, uint64(9), pvtStoreCommitHt)

	// commit the rightful next block
	assert.NoError(t, store.CommitWithPvtData(sampleData[9]))
	pvtStoreCommitHt, err = store.pvtdataStore.LastCommittedBlockHeight()
	assert.NoError(t, err)
	assert.Equal(t, uint64(10), pvtStoreCommitHt)
}

func TestAddAfterBlkStoreError(t *testing.T) {
	storeDir, err := ioutil.TempDir("", "lstore")
	if err != nil {
		t.Fatalf("Failed to create ledger storage directory: %s", err)
	}
	defer os.RemoveAll(storeDir)
	conf := buildPrivateDataConfig(storeDir)
	blockStoreDir := filepath.Join(storeDir, "chains")
	provider := NewProvider(blockStoreDir, conf, metricsProvider)
	defer provider.Close()
	store, err := provider.Open("testLedger")
	store.Init(btlPolicyForSampleData())
	defer store.Shutdown()
	assert.NoError(t, err)

	sampleData := sampleDataWithPvtdataForAllTxs(t)
	for _, d := range sampleData[0:9] {
		assert.NoError(t, store.CommitWithPvtData(d))
	}
	lastBlkAndPvtData := sampleData[9]
	// Add the block directly to blockstore
	store.BlockStore.AddBlock(lastBlkAndPvtData.Block)
	// Adding the same block should cause passing on the error caused by the block storgae
	assert.Error(t, store.CommitWithPvtData(lastBlkAndPvtData))
	// At the end, the pvt store status should be changed
	pvtStoreCommitHt, err := store.pvtdataStore.LastCommittedBlockHeight()
	assert.NoError(t, err)
	assert.Equal(t, uint64(10), pvtStoreCommitHt)
}

func TestPvtStoreAheadOfBlockStore(t *testing.T) {
	storeDir, err := ioutil.TempDir("", "lstore")
	if err != nil {
		t.Fatalf("Failed to create ledger storage directory: %s", err)
	}
	defer os.RemoveAll(storeDir)
	conf := buildPrivateDataConfig(storeDir)
	blockStoreDir := filepath.Join(storeDir, "chains")
	provider := NewProvider(blockStoreDir, conf, metricsProvider)
	defer provider.Close()
	store, err := provider.Open("testLedger")
	store.Init(btlPolicyForSampleData())
	defer store.Shutdown()
	assert.NoError(t, err)

	// when both stores are empty, isPvtstoreAheadOfBlockstore should be false
	assert.False(t, store.IsPvtStoreAheadOfBlockStore())

	sampleData := sampleDataWithPvtdataForSelectiveTx(t)
	for _, d := range sampleData[0:9] { // commit block number 0 to 8
		assert.NoError(t, store.CommitWithPvtData(d))
	}
	assert.False(t, store.IsPvtStoreAheadOfBlockStore())

	// close and reopen
	store.Shutdown()
	provider.Close()
	provider = NewProvider(blockStoreDir, conf, metricsProvider)
	store, err = provider.Open("testLedger")
	assert.NoError(t, err)
	store.Init(btlPolicyForSampleData())

	// as both stores are at the same block height, isPvtstoreAheadOfBlockstore should be false
	info, err := store.GetBlockchainInfo()
	assert.NoError(t, err)
	assert.Equal(t, uint64(9), info.Height)
	pvtStoreHt, err := store.pvtdataStore.LastCommittedBlockHeight()
	assert.NoError(t, err)
	assert.Equal(t, uint64(9), pvtStoreHt)
	assert.False(t, store.IsPvtStoreAheadOfBlockStore())

	lastBlkAndPvtData := sampleData[9]
	// Add the last block directly to the pvtdataStore but not to blockstore. This would make
	// the pvtdatastore height greater than the block store height.
	validTxPvtData, validTxMissingPvtData := constructPvtDataAndMissingData(lastBlkAndPvtData)
	err = store.pvtdataStore.Commit(lastBlkAndPvtData.Block.Header.Number, validTxPvtData, validTxMissingPvtData)
	assert.NoError(t, err)

	// close and reopen
	store.Shutdown()
	provider.Close()
	provider = NewProvider(blockStoreDir, conf, metricsProvider)
	store, err = provider.Open("testLedger")
	assert.NoError(t, err)
	store.Init(btlPolicyForSampleData())

	// pvtdataStore should be ahead of blockstore
	info, err = store.GetBlockchainInfo()
	assert.NoError(t, err)
	assert.Equal(t, uint64(9), info.Height)
	pvtStoreHt, err = store.pvtdataStore.LastCommittedBlockHeight()
	assert.NoError(t, err)
	assert.Equal(t, uint64(10), pvtStoreHt)
	assert.True(t, store.IsPvtStoreAheadOfBlockStore())

	// bring the height of BlockStore equal to pvtdataStore
	assert.NoError(t, store.CommitWithPvtData(lastBlkAndPvtData))
	info, err = store.GetBlockchainInfo()
	assert.NoError(t, err)
	assert.Equal(t, uint64(10), info.Height)
	pvtStoreHt, err = store.pvtdataStore.LastCommittedBlockHeight()
	assert.NoError(t, err)
	assert.Equal(t, uint64(10), pvtStoreHt)
	assert.False(t, store.IsPvtStoreAheadOfBlockStore())
}

func TestConstructPvtdataMap(t *testing.T) {
	assert.Nil(t, constructPvtdataMap(nil))
}

func sampleDataWithPvtdataForSelectiveTx(t *testing.T) []*ledger.BlockAndPvtData {
	var blockAndpvtdata []*ledger.BlockAndPvtData
	blocks := testutil.ConstructTestBlocks(t, 10)
	for i := 0; i < 10; i++ {
		blockAndpvtdata = append(blockAndpvtdata, &ledger.BlockAndPvtData{Block: blocks[i]})
	}

	// txNum 3, 5, 6 in block 2 has pvtdata but txNum 6 is invalid
	blockAndpvtdata[2].PvtData = samplePvtData(t, []uint64{3, 5, 6})
	txFilter := lutil.TxValidationFlags(blockAndpvtdata[2].Block.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER])
	txFilter.SetFlag(6, pb.TxValidationCode_INVALID_WRITESET)
	blockAndpvtdata[2].Block.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER] = txFilter

	// txNum 4, 6 in block 3 has pvtdata
	blockAndpvtdata[3].PvtData = samplePvtData(t, []uint64{4, 6})

	// txNum 4, 5 in block 5 has missing pvt data but txNum 5 is invalid
	missingData := make(ledger.TxMissingPvtDataMap)
	missingData.Add(4, "ns-4", "coll-4", true)
	missingData.Add(5, "ns-5", "coll-5", true)
	blockAndpvtdata[5].MissingPvtData = missingData
	txFilter = lutil.TxValidationFlags(blockAndpvtdata[5].Block.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER])
	txFilter.SetFlag(5, pb.TxValidationCode_INVALID_WRITESET)
	blockAndpvtdata[5].Block.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER] = txFilter

	return blockAndpvtdata
}

func sampleDataWithPvtdataForAllTxs(t *testing.T) []*ledger.BlockAndPvtData {
	var blockAndpvtdata []*ledger.BlockAndPvtData
	blocks := testutil.ConstructTestBlocks(t, 10)
	for i := 0; i < 10; i++ {
		blockAndpvtdata = append(blockAndpvtdata,
			&ledger.BlockAndPvtData{
				Block:   blocks[i],
				PvtData: samplePvtData(t, []uint64{uint64(i), uint64(i + 1)}),
			},
		)
	}
	return blockAndpvtdata
}

func samplePvtData(t *testing.T, txNums []uint64) map[uint64]*ledger.TxPvtData {
	pvtWriteSet := &rwset.TxPvtReadWriteSet{DataModel: rwset.TxReadWriteSet_KV}
	pvtWriteSet.NsPvtRwset = []*rwset.NsPvtReadWriteSet{
		{
			Namespace: "ns-1",
			CollectionPvtRwset: []*rwset.CollectionPvtReadWriteSet{
				{
					CollectionName: "coll-1",
					Rwset:          []byte("RandomBytes-PvtRWSet-ns1-coll1"),
				},
				{
					CollectionName: "coll-2",
					Rwset:          []byte("RandomBytes-PvtRWSet-ns1-coll2"),
				},
			},
		},
	}
	var pvtData []*ledger.TxPvtData
	for _, txNum := range txNums {
		pvtData = append(pvtData, &ledger.TxPvtData{SeqInBlock: txNum, WriteSet: pvtWriteSet})
	}
	return constructPvtdataMap(pvtData)
}

func btlPolicyForSampleData() pvtdatapolicy.BTLPolicy {
	return btltestutil.SampleBTLPolicy(
		map[[2]string]uint64{
			{"ns-1", "coll-1"}: 0,
			{"ns-1", "coll-2"}: 0,
		},
	)
}

func buildPrivateDataConfig(rootFSPath string) *pvtdatastorage.PrivateDataConfig {
	return &pvtdatastorage.PrivateDataConfig{
		PrivateDataConfig: &ledger.PrivateDataConfig{
			PurgeInterval: 1,
		},
		StorePath: filepath.Join(rootFSPath, "pvtdataStore"),
	}
}
