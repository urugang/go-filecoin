package chain

import (
	"context"
	"runtime/debug"
	"sync"

	"github.com/cskr/pubsub"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"

	bstore "github.com/ipfs/go-ipfs-blockstore"
	cbor "github.com/ipfs/go-ipld-cbor"
	logging "github.com/ipfs/go-log"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	"github.com/filecoin-project/go-filecoin/metrics/tracing"
	"github.com/filecoin-project/go-filecoin/repo"
	"github.com/filecoin-project/go-filecoin/types"
)

// NewHeadTopic is the topic used to publish new heads.
const NewHeadTopic = "new-head"

// GenesisKey is the key at which the genesis Cid is written in the datastore.
var GenesisKey = datastore.NewKey("/consensus/genesisCid")

var logStore = logging.Logger("chain.store")

var headKey = datastore.NewKey("/chain/heaviestTipSet")

// Store is a generic implementation of the Store interface.
// It works(tm) for now.
type Store struct {
	// bsPriv is the on disk storage for blocks.  This is private to
	// the Store to keep code that adds blocks to the Store's
	// underlying storage isolated to this module.  It is important that only
	// code with access to a Store can write to this storage to
	// simplify checking the security guarantee that only tipsets of a
	// validated chain are stored in the filecoin node's Store.
	bsPriv bstore.Blockstore
	// ds is the datastore backing bsPriv.  It is also accessed directly
	// to set and get chain meta-data, specifically the tipset cidset to
	// state root mapping, and the heaviest tipset cids.
	ds repo.Datastore

	// genesis is the CID of the genesis block.
	genesis cid.Cid
	// head is the tipset at the head of the best known chain.
	head types.TipSet
	// Protects head and genesisCid.
	mu sync.RWMutex

	// headEvents is a pubsub channel that publishes an event every time the head changes.
	// We operate under the assumption that tipsets published to this channel
	// will always be queued and delivered to subscribers in the order discovered.
	// Successive published tipsets may be supersets of previously published tipsets.
	// TODO: rename to notifications.  Also, reconsider ordering assumption depending
	// on decisions made around the FC node notification system.
	headEvents *pubsub.PubSub

	// Tracks tipsets by height/parentset for use by expected consensus.
	tipIndex *TipIndex
}

// NewStore constructs a new default store.
func NewStore(ds repo.Datastore, genesisCid cid.Cid) *Store {
	priv := bstore.NewBlockstore(ds)
	return &Store{
		bsPriv:     priv,
		ds:         ds,
		headEvents: pubsub.New(128),
		tipIndex:   NewTipIndex(),
		genesis:    genesisCid,
	}
}

// Load rebuilds the Store's caches by traversing backwards from the
// most recent best head as stored in its datastore.  Because Load uses a
// content addressed datastore it guarantees that parent blocks are correctly
// resolved from the datastore.  Furthermore Load ensures that all tipsets
// references correctly have the same parent height, weight and parent set.
// However, Load DOES NOT validate state transitions, it assumes that the
// tipset were only Put to the Store after checking for valid transitions.
//
// Furthermore Load trusts that the Store's backing datastore correctly
// preserves the cids of the heaviest tipset under the "headKey" datastore key.
// If the headKey cids are tampered with and invalid blocks added to the datastore
// then Load could be tricked into loading an invalid chain. Load will error if the
// head does not link back to the expected genesis block, or the Store's
// datastore does not store a link in the chain.  In case of error the caller
// should not consider the chain useable and propagate the error.
func (store *Store) Load(ctx context.Context) (err error) {
	ctx, span := trace.StartSpan(ctx, "Store.Load")
	defer tracing.AddErrorEndSpan(ctx, span, &err)

	// Clear the tipset index.
	store.tipIndex = NewTipIndex()

	headTsKey, err := store.loadHead()
	if err != nil {
		return err
	}

	headTs, err := LoadTipSetBlocks(ctx, store, headTsKey)
	if err != nil {
		return errors.Wrap(err, "error loading head tipset")
	}
	startHeight := headTs.At(0).Height
	logStore.Infof("start loading chain at tipset: %s, height: %d", headTsKey.String(), startHeight)
	// Ensure we only produce 10 log messages regardless of the chain height.
	logStatusEvery := uint64(startHeight / 10)

	var genesii types.TipSet
	// Provide tipsets directly from the block store, not from the tipset index which is
	// being rebuilt by this traversal.
	tipsetProvider := TipSetProviderFromBlocks(ctx, store)
	for iterator := IterAncestors(ctx, tipsetProvider, headTs); !iterator.Complete(); err = iterator.Next() {
		if err != nil {
			return err
		}

		height, err := iterator.Value().Height()
		if err != nil {
			return err
		}
		if logStatusEvery != 0 && (height%logStatusEvery) == 0 {
			logStore.Infof("load tipset: %s, height: %v", iterator.Value().String(), height)
		}
		stateRoot, err := store.loadStateRoot(iterator.Value())
		if err != nil {
			return err
		}
		err = store.PutTipSetAndState(ctx, &TipSetAndState{
			TipSet:          iterator.Value(),
			TipSetStateRoot: stateRoot,
		})
		if err != nil {
			return err
		}

		genesii = iterator.Value()
	}
	// Check genesis here.
	if genesii.Len() != 1 {
		return errors.Errorf("load terminated with tipset of %d blocks, expected genesis with exactly 1", genesii.Len())
	}

	loadCid := genesii.At(0).Cid()
	if !loadCid.Equals(store.genesis) {
		return errors.Errorf("expected genesis cid: %s, loaded genesis cid: %s", store.genesis, loadCid)
	}

	logStore.Infof("finished loading %d tipsets from %s", startHeight, headTs.String())
	// Set actual head.
	return store.SetHead(ctx, headTs)
}

// loadHead loads the latest known head from disk.
func (store *Store) loadHead() (types.SortedCidSet, error) {
	var emptyCidSet types.SortedCidSet
	bb, err := store.ds.Get(headKey)
	if err != nil {
		return emptyCidSet, errors.Wrap(err, "failed to read headKey")
	}

	var cids types.SortedCidSet
	err = cbor.DecodeInto(bb, &cids)
	if err != nil {
		return emptyCidSet, errors.Wrap(err, "failed to cast headCids")
	}

	return cids, nil
}

func (store *Store) loadStateRoot(ts types.TipSet) (cid.Cid, error) {
	h, err := ts.Height()
	if err != nil {
		return cid.Undef, err
	}
	key := datastore.NewKey(makeKey(ts.String(), h))
	bb, err := store.ds.Get(key)
	if err != nil {
		return cid.Undef, errors.Wrapf(err, "failed to read tipset key %s", ts.String())
	}

	var stateRoot cid.Cid
	err = cbor.DecodeInto(bb, &stateRoot)
	if err != nil {
		return cid.Undef, errors.Wrapf(err, "failed to cast state root of tipset %s", ts.String())
	}
	return stateRoot, nil
}

// putBlk persists a block to disk.
func (store *Store) putBlk(ctx context.Context, block *types.Block) error {
	if err := store.bsPriv.Put(block.ToNode()); err != nil {
		return errors.Wrap(err, "failed to put block")
	}
	return nil
}

// PutTipSetAndState persists the blocks of a tipset and the tipset index.
func (store *Store) PutTipSetAndState(ctx context.Context, tsas *TipSetAndState) error {
	// Persist blocks.
	for i := 0; i < tsas.TipSet.Len(); i++ {
		if err := store.putBlk(ctx, tsas.TipSet.At(i)); err != nil {
			return err
		}
	}

	// Update tipindex.
	err := store.tipIndex.Put(tsas)
	if err != nil {
		return err
	}
	// Persist the state mapping.
	if err = store.writeTipSetAndState(tsas); err != nil {
		return err
	}

	return nil
}

// GetTipSet returns the tipset whose block
// cids correspond to the input sorted cid set.
func (store *Store) GetTipSet(tsKey types.SortedCidSet) (types.TipSet, error) {
	return store.tipIndex.GetTipSet(tsKey.String())
}

// GetTipSetStateRoot returns the state of the tipset whose block
// cids correspond to the input sorted cid set.
func (store *Store) GetTipSetStateRoot(tsKey types.SortedCidSet) (cid.Cid, error) {
	return store.tipIndex.GetTipSetStateRoot(tsKey.String())
}

// HasTipSetAndState returns true iff the default store's tipindex is indexing
// the tipset referenced in the input key.
func (store *Store) HasTipSetAndState(ctx context.Context, tsKey string) bool {
	return store.tipIndex.Has(tsKey)
}

// GetTipSetAndStatesByParentsAndHeight returns the the tipsets and states tracked by
// the default store's tipIndex that have the parent set corresponding to the
// input key.
func (store *Store) GetTipSetAndStatesByParentsAndHeight(pTsKey string, h uint64) ([]*TipSetAndState, error) {
	return store.tipIndex.GetByParentsAndHeight(pTsKey, h)
}

// HasTipSetAndStatesWithParentsAndHeight returns true if the default store's tipindex
// contains any tipset indexed by the provided parent ID.
func (store *Store) HasTipSetAndStatesWithParentsAndHeight(pTsKey string, h uint64) bool {
	return store.tipIndex.HasByParentsAndHeight(pTsKey, h)
}

// GetBlocks retrieves the blocks referenced in the input cid set.
func (store *Store) GetBlocks(ctx context.Context, cids types.SortedCidSet) (blks []*types.Block, err error) {
	ctx, span := trace.StartSpan(ctx, "Store.GetBlocks")
	span.AddAttributes(trace.StringAttribute("tipset", cids.String()))
	defer tracing.AddErrorEndSpan(ctx, span, &err)

	var blocks []*types.Block
	for it := cids.Iter(); !it.Complete(); it.Next() {
		id := it.Value()
		block, err := store.GetBlock(ctx, id)
		if err != nil {
			return nil, errors.Wrap(err, "error fetching block")
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

// GetBlock retrieves a block by cid.
func (store *Store) GetBlock(ctx context.Context, c cid.Cid) (*types.Block, error) {
	data, err := store.bsPriv.Get(c)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get block %s", c.String())
	}
	return types.DecodeBlock(data.RawData())
}

// HasAllBlocks indicates whether the blocks are in the store.
func (store *Store) HasAllBlocks(ctx context.Context, cids []cid.Cid) bool {
	for _, c := range cids {
		if !store.HasBlock(ctx, c) {
			return false
		}
	}
	return true
}

// HasBlock indicates whether the block is in the store.
func (store *Store) HasBlock(ctx context.Context, c cid.Cid) bool {
	// Note: this redundantly decodes the block if it is found.
	blk, err := store.GetBlock(ctx, c)
	return blk != nil && err == nil
}

// HeadEvents returns a pubsub interface the pushes events each time the
// default store's head is reset.
func (store *Store) HeadEvents() *pubsub.PubSub {
	return store.headEvents
}

// SetHead sets the passed in tipset as the new head of this chain.
func (store *Store) SetHead(ctx context.Context, ts types.TipSet) error {
	logStore.Debugf("SetHead %s", ts.String())

	// Add logging to debug sporadic test failure.
	if !ts.Defined() {
		logStore.Error("publishing empty tipset")
		logStore.Error(debug.Stack())
	}

	if err := store.setHeadPersistent(ctx, ts); err != nil {
		return err
	}

	// Publish an event that we have a new head.
	store.HeadEvents().Pub(ts, NewHeadTopic)

	return nil
}

func (store *Store) setHeadPersistent(ctx context.Context, ts types.TipSet) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	// Ensure consistency by storing this new head on disk.
	if errInner := store.writeHead(ctx, ts.ToSortedCidSet()); errInner != nil {
		return errors.Wrap(errInner, "failed to write new Head to datastore")
	}

	store.head = ts

	return nil
}

// writeHead writes the given cid set as head to disk.
func (store *Store) writeHead(ctx context.Context, cids types.SortedCidSet) error {
	logStore.Debugf("WriteHead %s", cids.String())
	val, err := cbor.DumpObject(cids)
	if err != nil {
		return err
	}

	return store.ds.Put(headKey, val)
}

// writeTipSetAndState writes the tipset key and the state root id to the
// datastore.
func (store *Store) writeTipSetAndState(tsas *TipSetAndState) error {
	if tsas.TipSetStateRoot == cid.Undef {
		return errors.New("attempting to write state root cid.Undef")
	}

	val, err := cbor.DumpObject(tsas.TipSetStateRoot)
	if err != nil {
		return err
	}

	// datastore keeps tsKey:stateRoot (k,v) pairs.
	h, err := tsas.TipSet.Height()
	if err != nil {
		return err
	}
	key := datastore.NewKey(makeKey(tsas.TipSet.String(), h))
	return store.ds.Put(key, val)
}

// GetHead returns the current head tipset cids.
func (store *Store) GetHead() types.SortedCidSet {
	store.mu.RLock()
	defer store.mu.RUnlock()

	if !store.head.Defined() {
		return types.SortedCidSet{}
	}

	return store.head.ToSortedCidSet()
}

// BlockHeight returns the chain height of the head tipset.
// Strictly speaking, the block height is the number of tip sets that appear on chain plus
// the number of "null blocks" that occur when a mining round fails to produce a block.
func (store *Store) BlockHeight() (uint64, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	return store.head.Height()
}

// GenesisCid returns the genesis cid of the chain tracked by the default store.
func (store *Store) GenesisCid() cid.Cid {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.genesis
}

// Stop stops all activities and cleans up.
func (store *Store) Stop() {
	store.headEvents.Shutdown()
}
