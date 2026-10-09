package hardware

import (
	"sync"

	"github.com/wendy512/go-iecp5/asdu"
)

// pointKey correlates an inbound event to a previously sent command: IEC 104
// has no notion of caller identity, so (CommonAddr, InfoObjAddr) is the only
// stable join key.
type pointKey struct {
	ca  asdu.CommonAddr
	ioa asdu.InfoObjAddr
}

// registry remembers the Meta tag of the last command sent per address so that
// later spontaneous/reply events can be tagged with the same identity.
type registry struct {
	mu   sync.RWMutex
	meta map[pointKey]any
}

func newRegistry() *registry {
	return &registry{meta: make(map[pointKey]any)}
}

func (r *registry) store(ca asdu.CommonAddr, ioa asdu.InfoObjAddr, meta any) {
	if meta == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta[pointKey{ca, ioa}] = meta
}

func (r *registry) lookup(ca asdu.CommonAddr, ioa asdu.InfoObjAddr) any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.meta[pointKey{ca, ioa}]
}
