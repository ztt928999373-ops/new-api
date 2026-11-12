// Copyright (c) QuantumNous and contributors.
package credentials

import (
	"context"
	"sync"
)

// Single-process only: all managed status/content writers and resolvers share
// this barrier, including distinct Store/Resolver instances in the same node.
// It stores identifiers and cancellation state, never plaintext credentials.
type admissionBarrier struct {
	mu     sync.Mutex
	active map[*Lease]struct{}
}

var admissionBarriers sync.Map

func credentialAdmission(environment, id string) *admissionBarrier {
	g, _ := admissionBarriers.LoadOrStore(environment+"\x00"+id, &admissionBarrier{active: make(map[*Lease]struct{})})
	return g.(*admissionBarrier)
}

type Lease struct {
	ctx        context.Context
	cancel     context.CancelFunc
	barrier    *admissionBarrier
	version    uint64
	historical bool
	once       sync.Once
}

func (l *Lease) Context() context.Context { return l.ctx }

func (l *Lease) Version() uint64 { return l.version }

func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.barrier.mu.Lock()
		delete(l.barrier.active, l)
		l.cancel()
		l.barrier.mu.Unlock()
	})
}

// Called with mu held after a successful database commit. Cancellation cannot
// undo bytes already delivered upstream or promise removal of incurred charges.
func (g *admissionBarrier) invalidate(current uint64, unavailable bool) {
	for lease := range g.active {
		if unavailable || (!lease.historical && lease.version != current) {
			lease.cancel()
		}
	}
}

// HistoricalAuthorizer must validate an immutable server-side reference (for
// example a task's pinned credential/version and submitting identity). No HTTP
// entry point enables it in this restricted rollout. Nil means deny history.
type HistoricalAuthorizer interface {
	AuthorizeHistorical(context.Context, Principal, Record, string) error
}
