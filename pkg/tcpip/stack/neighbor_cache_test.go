// Copyright 2019 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package stack

import (
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/sleep"
	"gvisor.dev/gvisor/pkg/tcpip"
)

func newTestNeighborCache(dispatcher NUDDispatcher, config NUDConfigurations) *neighborCache {
	c := &neighborCache{
		nicID:      1,
		linkEP:     &testLinkEndpoint{},
		state:      NewNUDState(config.resetInvalidFields()),
		dispatcher: dispatcher,
	}
	c.mu.cache = make(map[tcpip.Address]*neighborEntry, neighborCacheSize)
	return c
}

// testEntryStore generates a default set of IP to MAC addresses used for
// testing and allows for modification of link addresses to simulate a new
// neighbor in the network reusing an IP address.
type testEntryStore struct {
	mu      sync.RWMutex
	entries map[tcpip.Address]NeighborEntry
}

func newTestEntryStore() *testEntryStore {
	entries := make(map[tcpip.Address]NeighborEntry)
	for i := 0; i < 4*neighborCacheSize; i++ {
		addr := fmt.Sprintf("Addr%06d", i)
		entries[tcpip.Address(addr)] = NeighborEntry{
			Addr:      tcpip.Address(addr),
			LocalAddr: tcpip.Address("LocalAddr"),
			LinkAddr:  tcpip.LinkAddress("Link" + addr),
		}
	}
	return &testEntryStore{
		entries: entries,
	}
}

func (s *testEntryStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

func (s *testEntryStore) Entry(i int) NeighborEntry {
	addr := fmt.Sprintf("Addr%06d", i)
	entry, _ := s.EntryByAddr(tcpip.Address(addr))
	return entry
}

func (s *testEntryStore) EntryByAddr(addr tcpip.Address) (NeighborEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[addr]
	return entry, ok
}

func (s *testEntryStore) Entries() []NeighborEntry {
	entries := make([]NeighborEntry, 0, len(s.entries))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := 0; i < 4*neighborCacheSize; i++ {
		addr := fmt.Sprintf("Addr%06d", i)
		if entry, ok := s.entries[tcpip.Address(addr)]; ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (s *testEntryStore) Set(i int, linkAddr tcpip.LinkAddress) {
	addr := fmt.Sprintf("Addr%06d", i)
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[tcpip.Address(addr)]; ok {
		entry.LinkAddr = linkAddr
		s.entries[tcpip.Address(addr)] = entry
	}
}

// testLinkAddressResolver implements LinkAddressResolver to emulate sending a
// neighbor probe.
type testLinkAddressResolver struct {
	handler              NUDHandler
	entries              *testEntryStore
	delay                time.Duration
	onLinkAddressRequest func()
}

var _ LinkAddressResolver = (*testLinkAddressResolver)(nil)

func (r *testLinkAddressResolver) LinkAddressRequest(addr, localAddr tcpip.Address, linkAddr tcpip.LinkAddress, linkEP LinkEndpoint) *tcpip.Error {
	time.AfterFunc(r.delay, func() { r.fakeRequest(addr) })
	if f := r.onLinkAddressRequest; f != nil {
		f()
	}
	return nil
}

func (r *testLinkAddressResolver) fakeRequest(addr tcpip.Address) {
	if entry, ok := r.entries.EntryByAddr(addr); ok {
		r.handler.HandleConfirmation(entry.Addr, entry.LinkAddr, true, false, false)
	}
}

func (*testLinkAddressResolver) ResolveStaticAddress(addr tcpip.Address) (tcpip.LinkAddress, bool) {
	if addr == "broadcast" {
		return "mac_broadcast", true
	}
	return "", false
}

func (*testLinkAddressResolver) LinkAddressProtocol() tcpip.NetworkProtocolNumber {
	return 1
}

func getBlocking(n *neighborCache, e NeighborEntry, linkRes LinkAddressResolver) (NeighborEntry, *tcpip.Error) {
	w := sleep.Waker{}
	s := sleep.Sleeper{}
	s.AddWaker(&w, 123)
	defer s.Done()

	for {
		if got, _, err := n.entry(e.Addr, e.LocalAddr, linkRes, &w); err != tcpip.ErrWouldBlock {
			return got, err
		}
		s.Fetch(true)
	}
}

// testLinkEndpoint implements LinkEndpoint to validate the sending of probes
// and advertisements upon each certain NUD events.
type testLinkEndpoint struct {
	LinkEndpoint
}

type entryEvent struct {
	NICID    tcpip.NICID
	Address  tcpip.Address
	LinkAddr tcpip.LinkAddress
	State    NeighborState
}

// testNUDDispatcher implements NUDDispatcher to validate the dispatching of
// events upon certain NUD state machine events.
type testNUDDispatcher struct{}

var _ NUDDispatcher = (*testNUDDispatcher)(nil)

func (t *testNUDDispatcher) OnNeighborAdded(nicID tcpip.NICID, ipAddr tcpip.Address, linkAddr tcpip.LinkAddress, state NeighborState) {
	if testing.Verbose() {
		log.Printf("Neighbor %q added w/ nic=%d, linkAddr=%q, state=%s\n", string(ipAddr), nicID, linkAddr, state)
	}
}

func (t *testNUDDispatcher) OnNeighborStateChange(nicID tcpip.NICID, ipAddr tcpip.Address, linkAddr tcpip.LinkAddress, state NeighborState) {
	if testing.Verbose() {
		log.Printf("Neighbor %q changed w/ nic=%d, linkAddr=%q, state=%s\n", string(ipAddr), nicID, string(linkAddr), state)
	}
}
func (t *testNUDDispatcher) OnNeighborRemoved(nicID tcpip.NICID, ipAddr tcpip.Address, linkAddr tcpip.LinkAddress, state NeighborState) {
	if testing.Verbose() {
		log.Printf("Neighbor %q removed w/ nic=%d, linkAddr=%q, state=%s\n", string(ipAddr), nicID, string(linkAddr), state)
	}
}

func TestCacheOverflowDynamic(t *testing.T) {
	disp := &testNUDDispatcher{}
	neigh := newTestNeighborCache(disp, NUDConfigurations{
		BaseReachableTime: time.Duration(math.MaxInt64), // approximately 292 years
	})
	store := newTestEntryStore()
	linkRes := &testLinkAddressResolver{
		handler: neigh,
		entries: store,
		delay:   0,
	}
	// Add more static entries than the neighbor cache can hold.
	for i := store.Len() - 1; i >= 0; i-- {
		a := store.Entry(i)
		neigh.addStaticEntry(a.Addr, a.LinkAddr)
		e, _, err := neigh.entry(a.Addr, a.LocalAddr, linkRes, nil)
		if err != nil {
			t.Errorf("insert %d, neigh.entry(%q)=%q, got error: %v", i, string(a.Addr), e.LinkAddr, err)
		}
		if e.LinkAddr != a.LinkAddr {
			t.Errorf("insert %d, neigh.entry(%q)=%q, want %q", i, string(a.Addr), e.LinkAddr, a.LinkAddr)
		}
	}
	// Expect to find at least half of the most recent entries.
	for i := 0; i < neighborCacheSize/2; i++ {
		a := store.Entry(i)
		e, _, err := neigh.entry(a.Addr, a.LocalAddr, linkRes, nil)
		if err != nil {
			t.Errorf("insert %d, neigh.entry(%q)=%q, got error: %v", i, string(a.Addr), e.LinkAddr, err)
		}
		if e.LinkAddr != a.LinkAddr {
			t.Errorf("insert %d, neigh.entry(%q)=%q, want %q", i, string(a.Addr), e.LinkAddr, a.LinkAddr)
		}
	}
	// The earliest entries should no longer be in the cache.
	entries := neigh.entries()
	hasAddr := make(map[tcpip.Address]struct{})
	for _, e := range entries {
		hasAddr[e.Addr] = struct{}{}
	}
	for i := store.Len() - 1; i >= store.Len()-neighborCacheSize; i-- {
		addr := store.Entry(i).Addr
		if _, ok := hasAddr[addr]; ok {
			t.Errorf("check %d, neigh.entry(%q), got exists, want nonexistent", i, string(addr))
		}
	}
}

func TestCacheConcurrent(t *testing.T) {
	disp := &testNUDDispatcher{}
	neigh := newTestNeighborCache(disp, NUDConfigurations{
		BaseReachableTime: time.Duration(1<<63 - 1), // approximately 290 years
	})
	store := newTestEntryStore()
	linkRes := &testLinkAddressResolver{
		handler: neigh,
		entries: store,
		delay:   0,
	}

	storeEntries := store.Entries()
	var wg sync.WaitGroup
	for r := 0; r < 16; r++ {
		wg.Add(1)
		go func(r int) {
			for _, e := range storeEntries {
				_, doneCh, err := neigh.entry(e.Addr, e.LocalAddr, linkRes, nil)
				if err != nil && err != tcpip.ErrWouldBlock {
					t.Errorf("neigh.entry(%q) want success or ErrWouldBlock, got %v", string(e.Addr), err)
				}
				if doneCh != nil {
					<-doneCh
				}
			}
			wg.Done()
		}(r)
	}
	wg.Wait()

	entries := make(map[tcpip.Address]NeighborEntry)
	for _, e := range neigh.entries() {
		entries[e.Addr] = e
	}

	// All goroutines add in the same order and add more values than
	// can fit in the cache, so our eviction strategy requires that
	// the last entry be present and the first be missing.
	e := store.Entry(store.Len() - 1)
	if entry, ok := entries[e.Addr]; ok {
		if entry.LinkAddr != e.LinkAddr {
			t.Errorf("neigh.entry(%q)=%q, want %q", string(e.Addr), entry.LinkAddr, e.LinkAddr)
		}
	} else {
		t.Errorf("neigh.entry(%q) does not exists, want exists", string(e.Addr))
	}

	e = store.Entry(0)
	if _, ok := entries[e.Addr]; ok {
		t.Errorf("neigh.entry(%q) exists, want does not exist", string(e.Addr))
	}
}

func TestCacheAgeLimit(t *testing.T) {
	neigh := newTestNeighborCache(nil, NUDConfigurations{
		BaseReachableTime: time.Millisecond,
		UnreachableTime:   time.Millisecond,
	})
	store := newTestEntryStore()
	linkRes := &testLinkAddressResolver{
		handler: neigh,
		entries: store,
		delay:   0,
	}

	e := store.Entry(0)
	if _, _, err := neigh.entry(e.Addr, e.LocalAddr, linkRes, nil); err != tcpip.ErrWouldBlock {
		t.Errorf("neigh.entry(%q) exists, want does not exist: %v", string(e.Addr), err)
	}
	time.Sleep(3 * time.Millisecond)
	if entry, _, err := neigh.entry(e.Addr, e.LocalAddr, nil, nil); err != tcpip.ErrNoLinkAddress {
		t.Errorf("neigh.entry(%q) exists, want does not exist after timeout: %v", string(e.Addr), entry)
	}
}

func TestCacheReplace(t *testing.T) {
	disp := &testNUDDispatcher{}
	neigh := newTestNeighborCache(disp, NUDConfigurations{
		BaseReachableTime:   time.Duration(1<<63 - 1), // approximately 290 years
		DelayFirstProbeTime: time.Millisecond,
		RetransmitTimer:     time.Millisecond,
	})
	store := newTestEntryStore()
	linkRes := &testLinkAddressResolver{
		handler: neigh,
		entries: store,
		delay:   0,
	}

	a := store.Entry(0)
	e, doneCh, err := neigh.entry(a.Addr, a.LocalAddr, linkRes, nil)
	if err != nil && doneCh != nil {
		<-doneCh
		if e2, _, err := neigh.entry(a.Addr, a.LocalAddr, linkRes, nil); err != nil {
			t.Errorf("neigh.entry(%q) does not exist, got %v", string(a.Addr), err)
		} else {
			e = e2
		}
	} else {
		t.Errorf("neigh.entry(%q) should exist, got %v", string(a.Addr), err)
	}
	if e.LinkAddr != a.LinkAddr {
		t.Errorf("neigh.entry(%q).LinkAddr = %q, want %q", string(a.Addr), string(e.LinkAddr), string(a.LinkAddr))
	}

	updatedLinkAddr := a.LinkAddr + "2"
	store.Set(0, updatedLinkAddr)
	neigh.HandleConfirmation(a.Addr, updatedLinkAddr, false, true, false)
	e, doneCh, err = neigh.entry(a.Addr, a.LocalAddr, linkRes, nil)
	if doneCh != nil && err == tcpip.ErrWouldBlock {
		<-doneCh
	} else {
		t.Errorf("neigh.entry(%q) should block, got %v", string(a.Addr), err)
	}
	e, _, err = neigh.entry(a.Addr, a.LocalAddr, linkRes, nil)
	if err != nil {
		t.Errorf("neigh.entry(%q) should exist, got %v", string(a.Addr), err)
	}
	if e.LinkAddr != updatedLinkAddr {
		t.Errorf("neigh.entry(%q).LinkAddr = %q, want %q", string(a.Addr), string(e.LinkAddr), string(updatedLinkAddr))
	}
}

func TestCacheResolution(t *testing.T) {
	disp := &testNUDDispatcher{}
	neigh := newTestNeighborCache(disp, NUDConfigurations{
		BaseReachableTime: time.Duration(math.MaxInt64), // approximately 292 years
	})
	store := newTestEntryStore()
	linkRes := &testLinkAddressResolver{
		handler: neigh,
		entries: store,
		delay:   0,
	}

	for _, e := range store.Entries() {
		got, err := getBlocking(neigh, e, linkRes)
		if err != nil {
			t.Errorf("neigh.entry(%q) got error: %v, want error: %v", string(e.Addr), err, tcpip.ErrWouldBlock)
		}
		if got.LinkAddr != e.LinkAddr {
			t.Errorf("neigh.entry(%q)=%q, want %q", string(e.Addr), string(got.LinkAddr), string(e.LinkAddr))
		}
	}

	// Check that after resolved, address stays in the cache and never returns WouldBlock.
	for i := 0; i < 10; i++ {
		e := store.Entry(store.Len() - 1)
		got, _, err := neigh.entry(e.Addr, e.LocalAddr, linkRes, nil)
		if err != nil {
			t.Errorf("neigh.entry(%q)=%q, got error: %v", string(e.Addr), got, err)
		}
		if got.LinkAddr != e.LinkAddr {
			t.Errorf("neigh.entry(%q)=%q, want %q", string(e.Addr), got.LinkAddr, e.LinkAddr)
		}
	}
}

func TestCacheResolutionFailed(t *testing.T) {
	disp := &testNUDDispatcher{}
	neigh := newTestNeighborCache(disp, NUDConfigurations{
		BaseReachableTime: time.Duration(math.MaxInt64), // approximately 292 years
		RetransmitTimer:   time.Millisecond,
	})
	store := newTestEntryStore()

	var requestCount uint32
	linkRes := &testLinkAddressResolver{
		handler: neigh,
		entries: store,
		delay:   0,
		onLinkAddressRequest: func() {
			atomic.AddUint32(&requestCount, 1)
		},
	}

	// First, sanity check that resolution is working
	e := store.Entry(0)
	got, err := getBlocking(neigh, e, linkRes)
	if err != nil {
		t.Errorf("neigh.entry(%q) got error: %v, want error: ErrWouldBlock", string(e.Addr), err)
	}
	if got.LinkAddr != e.LinkAddr {
		t.Errorf("neigh.entry(%q)=%q, want %q", string(e.Addr), string(got.LinkAddr), string(e.LinkAddr))
	}

	before := atomic.LoadUint32(&requestCount)

	e.Addr += "2"
	if _, err := getBlocking(neigh, e, linkRes); err != tcpip.ErrNoLinkAddress {
		t.Errorf("neigh.entry(%q) got error: %v, want error: ErrNoLinkAddress", string(e.Addr), err)
	}

	maxAttempts := neigh.config().MaxUnicastProbes
	if got, want := atomic.LoadUint32(&requestCount)-before, maxAttempts; got != want {
		t.Errorf("got link address request count = %d, want = %d", got, want)
	}
}

// TestCacheResolutionTimeout simulates sending MaxMulticastProbes probes and
// not retrieving a confirmation before the duration defined by
// MaxMulticastProbes * RetransmitTimer.
func TestCacheResolutionTimeout(t *testing.T) {
	neigh := newTestNeighborCache(nil, NUDConfigurations{
		BaseReachableTime:  time.Duration(1<<63 - 1), // approximately 290 years
		RetransmitTimer:    time.Millisecond,
		MaxMulticastProbes: 3,
	})
	store := newTestEntryStore()
	linkRes := &testLinkAddressResolver{
		handler: neigh,
		entries: store,
		delay:   time.Second,
	}

	e := store.Entry(0)
	if _, err := getBlocking(neigh, e, linkRes); err != tcpip.ErrNoLinkAddress {
		t.Errorf("neigh.entry(%q) got error: %v, want error: ErrNoLinkAddress", string(e.Addr), err)
	}
}

// TestStaticResolution checks that static link addresses are resolved
// immediately and don't send resolution requests.
func TestStaticResolution(t *testing.T) {
	neigh := newTestNeighborCache(nil, NUDConfigurations{
		BaseReachableTime:  time.Duration(1<<63 - 1), // approximately 290 years
		RetransmitTimer:    time.Millisecond,
		MaxMulticastProbes: 3,
	})
	store := newTestEntryStore()
	linkRes := &testLinkAddressResolver{
		handler: neigh,
		entries: store,
		delay:   time.Minute,
	}

	addr := tcpip.Address("broadcast")
	localAddr := tcpip.Address("LocalAddr")
	want := tcpip.LinkAddress("mac_broadcast")
	got, _, err := neigh.entry(addr, localAddr, linkRes, nil)
	if err != nil {
		t.Errorf("neigh.entry(%q)=%q, got error: %v", string(addr), got, err)
	}
	if got.LinkAddr != want {
		t.Errorf("neigh.entry(%q)=%q, want %q", string(addr), got.LinkAddr, want)
	}
}
