package dhtcrawler

import (
	"context"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/blocking"
	"github.com/bitmagnet-io/bitmagnet/internal/bloom"
	"github.com/bitmagnet-io/bitmagnet/internal/concurrency"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dao"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/client"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/ktable"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/metainfo"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/metainfo/banning"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/metainfo/metainforequester"
	"github.com/bitmagnet-io/bitmagnet/internal/rand"
	"github.com/prometheus/client_golang/prometheus"
	boom "github.com/tylertreat/BoomFilters"
	"go.uber.org/zap"
)

type crawler struct {
	kTable                       ktable.Table
	kTable6                      ktable.Table
	client                       client.Client
	metainfoRequester            metainforequester.Requester
	banningChecker               banning.Checker
	bootstrapNodes               []string
	reseedBootstrapNodesInterval time.Duration
	getOldestNodesInterval       time.Duration
	oldPeerThreshold             time.Duration
	discoveredNodes              concurrency.BatchingChannel[ktable.Node]
	nodesForPing                 concurrency.BufferedConcurrentChannel[ktable.Node]
	nodesForFindNode             concurrency.BufferedConcurrentChannel[ktable.Node]
	nodesForSampleInfoHashes     concurrency.BufferedConcurrentChannel[ktable.Node]
	infoHashTriage               concurrency.BatchingChannel[nodeHasPeersForHash]
	getPeers                     concurrency.BufferedConcurrentChannel[nodeHasPeersForHash]
	scrape                       concurrency.BufferedConcurrentChannel[nodeHasPeersForHash]
	requestMetaInfo              concurrency.BufferedConcurrentChannel[infoHashWithPeers]
	persistTorrents              concurrency.BatchingChannel[infoHashWithMetaInfo]
	persistSources               concurrency.BatchingChannel[infoHashWithScrape]
	rescrapeThreshold            time.Duration
	saveFilesThreshold           uint
	savePieces                   bool
	dao                          *dao.Query
	// ignoreHashes is a thread-safe bloom filter that the crawler keeps in memory,
	// containing every hash it has already encountered.
	// This avoids multiple attempts to crawl the same hash, and takes a lot of load off the database query
	// that checks if a hash has already been indexed.
	ignoreHashes    *ignoreHashes
	blockingManager blocking.Manager
	// soughtNodeID is a random node ID used as the target for find_node and sample_infohashes requests.
	// It is rotated every 10 seconds.
	soughtNodeID   *concurrency.AtomicValue[protocol.ID]
	stopped        chan struct{}
	persistedTotal *prometheus.CounterVec
	logger         *zap.SugaredLogger
	rand           *rand.CryptoSeededRand
}

func (c *crawler) start() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// start the various pipeline workers
	go c.rotateSoughtNodeID(ctx)
	go c.runDiscoveredNodes(ctx)
	go c.runPing(ctx)
	go c.runFindNode(ctx)
	go c.getNodesForFindNode(ctx)
	go c.runSampleInfoHashes(ctx)
	go c.getNodesForSampleInfoHashes(ctx)
	go c.runInfoHashTriage(ctx)
	go c.runGetPeers(ctx)
	go c.runRequestMetaInfo(ctx)
	go c.runScrape(ctx)
	go c.reseedBootstrapNodes(ctx)
	go c.runPersistTorrents(ctx)
	go c.runPersistSources(ctx)
	go c.getOldNodes(ctx)
	<-c.stopped
}

type nodeHasPeersForHash struct {
	infoHash protocol.ID
	node     netip.AddrPort
}

type infoHashWithMetaInfo struct {
	nodeHasPeersForHash
	metaInfo metainfo.Info
}

type infoHashWithPeers struct {
	nodeHasPeersForHash
	peers []netip.AddrPort
}

type infoHashWithScrape struct {
	nodeHasPeersForHash
	bfsd bloom.Filter
	bfpe bloom.Filter
}

type ignoreHashes struct {
	mutex sync.Mutex
	bloom *boom.StableBloomFilter
}

func (i *ignoreHashes) testAndAdd(id protocol.ID) bool {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	return i.bloom.TestAndAdd(id[:])
}

func (c *crawler) rotateSoughtNodeID(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
			c.soughtNodeID.Set(protocol.RandomNodeID())
		}
	}
}

func (c *crawler) getTableForIpFamily(addr netip.Addr) ktable.Table {
	if addr.Is4() {
		return c.kTable
	}
	return c.kTable6
}

func concatResultFromBothTables[R any](c *crawler, callable func(table ktable.Table) []R) []R {
	result4 := callable(c.kTable)
	result6 := callable(c.kTable6)
	concat := slices.Concat(result4, result6)
	if c.rand != nil {
		rand.Shuffle(c.rand, concat)
	}
	return concat
}
