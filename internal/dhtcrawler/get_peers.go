package dhtcrawler

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/ktable"
)

func (c *crawler) runGetPeers(ctx context.Context) {
	_ = c.getPeers.Run(ctx, func(req nodeHasPeersForHash) {
		pfh, pfhErr := c.requestPeersForHash(ctx, req)
		if pfhErr != nil {
			return
		}

		peers := make([]netip.AddrPort, 0, len(pfh.peers))
		hashPeers4 := make([]ktable.HashPeer, 0, len(pfh.peers))
		hashPeers6 := make([]ktable.HashPeer, 0, len(pfh.peers))
		for _, p := range pfh.peers {
			peers = append(peers, p)
			if p.Addr().Is4() {
				hashPeers4 = append(hashPeers4, ktable.HashPeer{
					Addr: p,
				})
			} else {
				hashPeers6 = append(hashPeers6, ktable.HashPeer{
					Addr: p,
				})
			}
		}

		c.kTable.BatchCommand(
			ktable.PutHash{ID: req.infoHash, Peers: hashPeers4},
		)
		c.kTable6.BatchCommand(
			ktable.PutHash{ID: req.infoHash, Peers: hashPeers6},
		)

		select {
		case <-ctx.Done():
			return
		case c.requestMetaInfo.In() <- infoHashWithPeers{
			nodeHasPeersForHash: req,
			peers:               peers,
		}:
			return
		}
	})
}

func (c *crawler) requestPeersForHash(
	ctx context.Context,
	req nodeHasPeersForHash,
) (infoHashWithPeers, error) {
	table := c.getTableForIpFamily(req.node.Addr())
	res, err := c.client.GetPeers(ctx, req.node, req.infoHash)
	if err != nil {
		table.BatchCommand(ktable.DropAddr{
			Addr:   req.node.Addr(),
			Reason: fmt.Errorf("failed to get peers: %w", err),
		})

		return infoHashWithPeers{}, err
	}

	table.BatchCommand(ktable.PutNode{
		ID:      res.ID,
		Addr:    req.node,
		Options: []ktable.NodeOption{ktable.NodeResponded()},
	})

	if len(res.Nodes) > 0 {
		// block the channel for up to a second in an attempt to add the nodes to the discoveredNodes channel
		cancelCtx, cancel := context.WithTimeout(ctx, time.Second)

	LOOP:
		for _, n := range res.Nodes {
			select {
			case <-cancelCtx.Done():
				break LOOP
			case c.discoveredNodes.In() <- ktable.NewNode(n.ID, n.Addr):
				continue
			}
		}

		cancel()
	}

	if len(res.Values) < 1 {
		return infoHashWithPeers{}, errors.New("no peers found")
	}

	return infoHashWithPeers{
		nodeHasPeersForHash: req,
		peers:               res.Values,
	}, nil
}
