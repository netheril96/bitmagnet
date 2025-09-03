package rand

import (
	"crypto/rand"
	"encoding/binary"
	mrand "math/rand"
	"sync"
)

type CryptoSeededRand struct {
	rng *mrand.Rand
	mu  sync.Mutex
}

// NewCryptoSeededRand creates a *rand.Rand seeded with crypto/rand.
func NewCryptoSeededRand() *CryptoSeededRand {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("failed to read crypto seed: " + err.Error())
	}
	seed := int64(binary.LittleEndian.Uint64(b[:]))
	return &CryptoSeededRand{
		rng: mrand.New(mrand.NewSource(seed)),
	}
}

// Shuffle shuffles a slice of any type in place, thread-safe.
func Shuffle[T any](c *CryptoSeededRand, s []T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.rng.Shuffle(len(s), func(i, j int) {
		s[i], s[j] = s[j], s[i]
	})
}
