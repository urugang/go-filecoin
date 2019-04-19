package node

import (
	"context"

	"gx/ipfs/QmR8BauakNcBa3RbE4nbQu76PDiJgoQgz8AJdhJuiU4TAw/go-cid"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"

	"go.opencensus.io/trace"

	"github.com/filecoin-project/go-filecoin/net/pubsub"
	"github.com/filecoin-project/go-filecoin/types"
)

// BlockTopic is the pubsub topic identifier on which new blocks are announced.
const BlockTopic = "/fil/blocks"

// AddNewBlock receives a newly mined block and stores, validates and propagates it to the network.
func (node *Node) AddNewBlock(ctx context.Context, b *types.Block) (err error) {
	ctx, span := trace.StartSpan(ctx, "Node.AddNewBlock")
	defer func() {
		if err != nil {
			span.AddAttributes(trace.StringAttribute("error", err.Error()))
		}
		span.End()
	}()
	span.AddAttributes(
		trace.StringAttribute("block", b.Cid().String()),
		trace.StringAttribute("miner", b.Miner.String()),
		trace.Int64Attribute("height", int64(b.Height)),
		trace.Int64Attribute("nonce", int64(b.Nonce)),
	)

	// Put block in storage wired to an exchange so this node and other
	// nodes can fetch it.
	log.Debugf("putting block in bitswap exchange: %s", b.Cid().String())
	blkCid, err := node.cborStore.Put(ctx, b)
	if err != nil {
		return errors.Wrap(err, "could not add new block to online storage")
	}

	log.Debugf("syncing new block: %s", b.Cid().String())
	if err := node.Syncer.HandleNewBlocks(ctx, []cid.Cid{blkCid}); err != nil {
		return err
	}

	// TODO: should this just be a cid? Right now receivers ask to fetch
	// the block over bitswap anyway.
	return node.PorcelainAPI.PubSubPublish(BlockTopic, b.ToNode().RawData())
}

func (node *Node) processBlock(ctx context.Context, pubSubMsg pubsub.Message) (err error) {
	// ignore messages from ourself
	if pubSubMsg.GetFrom() == node.Host().ID() {
		return nil
	}
	ctx, span := trace.StartSpan(ctx, "Node.processNewBlock")
	defer func() {
		if err != nil {
			span.AddAttributes(trace.StringAttribute("error", err.Error()))
		}
		span.End()
	}()

	blk, err := types.DecodeBlock(pubSubMsg.GetData())
	if err != nil {
		return errors.Wrap(err, "got bad block data")
	}

	span.AddAttributes(
		trace.StringAttribute("block", blk.Cid().String()),
		trace.StringAttribute("miner", blk.Miner.String()),
		trace.Int64Attribute("height", int64(blk.Height)),
		trace.Int64Attribute("nonce", int64(blk.Nonce)),
	)

	log.Infof("Received new block from network cid: %s", blk.Cid().String())
	log.Debugf("Received new block from network: %s", blk)

	err = node.Syncer.HandleNewBlocks(ctx, []cid.Cid{blk.Cid()})
	if err != nil {
		return errors.Wrap(err, "processing block from network")
	}

	return nil
}
