// Copyright 2020 The gVisor Authors.
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
	"math"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
)

const (
	entryTestNetNumber tcpip.NetworkProtocolNumber = math.MaxUint32

	entryTestNICID tcpip.NICID = 1
	entryTestAddr1             = tcpip.Address("\x0a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01")
	entryTestAddr2             = tcpip.Address("\x0a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02")

	entryTestLinkAddr1 = tcpip.LinkAddress("a")
	entryTestLinkAddr2 = tcpip.LinkAddress("b")

	// entryTestNetDefaultMTU is the MTU, in bytes, used throughout the tests,
	// except where another value is explicitly used. It is chosen to match the
	// MTU of loopback interfaces on Linux systems.
	entryTestNetDefaultMTU = 65536

	entryTestMaxDuration = time.Duration(math.MaxInt64) // approximately 290 years
)

// The following unit tests exercise every state transition and verify its
// behavior with RFC 4681.
//
// TODO(sbalana): For transitions that are caused by "probe or confirmation w/
// different address", enumerate all the confirmation flags (solicited,
// override, isRouter) to ensure thorough test coverage.
//
// | From       | To         | Cause                                      | Action                         | Event Dispatched |
// | ========== | ========== | ========================================== | ============================== | ================ |
// | Unknown    | Incomplete | Packet queued to unknown address           | Send probe                     | Added            |
// | Unknown    | Stale      | Probe received from unknown link address   |                                | Added            |
// | Incomplete | Incomplete | Retransmit timer expired                   | Send probe                     | StateChange      |
// | Incomplete | Reachable  | Solicited confirmation                     | Notify wakers                  | StateChange      |
// | Incomplete | Stale      | Unsolicited confirmation                   | Notify wakers                  | StateChange      |
// | Incomplete | Failed     | Max probes sent without reply              | Notify wakers, Send ICMP error | Removed          |
// | Reachable  | Stale      | Reachable timer expired                    |                                | StateChange      |
// | Reachable  | Stale      | Probe or confirmation w/ different address |                                | StateChange      |
// | Stale      | Delay      | Packet sent                                |                                | StateChange      |
// | Delay      | Reachable  | Upper-layer confirmation                   |                                | StateChange      |
// | Delay      | Stale      | Probe or confirmation w/ different address |                                | StateChange      |
// | Delay      | Probe      | Delay timer expired                        | Send probe                     | StateChange      |
// | Probe      | Probe      | Retransmit timer expired                   | Send probe                     | StateChange      |
// | Probe      | Stale      | Probe or confirmation w/ different address |                                | StateChange      |
// | Probe      | Reachable  | Solicited confirmation w/ same address     | Notify wakers                  | StateChange      |
// | Probe      | Failed     | Max probes sent without reply              | Notify wakers                  | Removed          |
// | Failed     | Unknown    | Unreachability timer expired               |                                |                  |

type entryTestEventType uint8

const (
	entryTestAdded entryTestEventType = iota
	entryTestStateChange
	entryTestRemoved
)

func (t entryTestEventType) String() string {
	switch t {
	case entryTestAdded:
		return "add"
	case entryTestStateChange:
		return "change"
	case entryTestRemoved:
		return "remove"
	default:
		return fmt.Sprintf("unknown (%d)", t)
	}
}

type entryTestEventInfo struct {
	eventType entryTestEventType
	nicID     tcpip.NICID
	addr      tcpip.Address
	linkAddr  tcpip.LinkAddress
	state     NeighborState
}

func (e entryTestEventInfo) String() string {
	switch e.eventType {
	case entryTestAdded:
		return fmt.Sprintf("add event for NIC #%d, addr=%q, linkAddr=%q, state=%q", e.nicID, e.addr, e.linkAddr, e.state)
	case entryTestStateChange:
		return fmt.Sprintf("change event for NIC #%d, addr=%q, linkAddr=%q, state=%q", e.nicID, e.addr, e.linkAddr, e.state)
	case entryTestRemoved:
		return fmt.Sprintf("remove event for NIC #%d, addr=%q, linkAddr=%q, state=%q", e.nicID, e.addr, e.linkAddr, e.state)
	default:
		return fmt.Sprintf("unknown event (%d)", e.eventType)
	}
}

// entryTestDispatcher implements NUDDispatcher to validate the dispatching of
// events upon certain NUD state machine events.
type entryTestDispatcher struct {
	// C is where events are queued
	C chan entryTestEventInfo
}

var _ NUDDispatcher = (*entryTestDispatcher)(nil)

func (t *entryTestDispatcher) OnNeighborAdded(nicID tcpip.NICID, addr tcpip.Address, linkAddr tcpip.LinkAddress, state NeighborState) {
	e := entryTestEventInfo{
		eventType: entryTestAdded,
		nicID:     nicID,
		addr:      addr,
		linkAddr:  linkAddr,
		state:     state,
	}
	select {
	case t.C <- e:
	default:
	}
}

func (t *entryTestDispatcher) OnNeighborStateChange(nicID tcpip.NICID, addr tcpip.Address, linkAddr tcpip.LinkAddress, state NeighborState) {
	e := entryTestEventInfo{
		eventType: entryTestStateChange,
		nicID:     nicID,
		addr:      addr,
		linkAddr:  linkAddr,
		state:     state,
	}
	select {
	case t.C <- e:
	default:
	}
}

func (t *entryTestDispatcher) OnNeighborRemoved(nicID tcpip.NICID, addr tcpip.Address, linkAddr tcpip.LinkAddress, state NeighborState) {
	e := entryTestEventInfo{
		eventType: entryTestRemoved,
		nicID:     nicID,
		addr:      addr,
		linkAddr:  linkAddr,
		state:     state,
	}
	select {
	case t.C <- e:
	default:
	}
}

type entryTestLinkResolver struct {
	// C is where output probes are queued
	C chan entryTestProbeInfo
}

var _ LinkAddressResolver = (*entryTestLinkResolver)(nil)

type entryTestProbeInfo struct {
	RemoteAddress     tcpip.Address
	RemoteLinkAddress tcpip.LinkAddress
	LocalAddress      tcpip.Address
}

// LinkAddressRequest sends a request for the LinkAddress of addr. Broadcasts
// to the local network if linkAddr is the zero value.
func (r *entryTestLinkResolver) LinkAddressRequest(addr, localAddr tcpip.Address, linkAddr tcpip.LinkAddress, linkEP LinkEndpoint) *tcpip.Error {
	p := entryTestProbeInfo{
		RemoteAddress:     addr,
		RemoteLinkAddress: linkAddr,
		LocalAddress:      localAddr,
	}

	select {
	case r.C <- p:
		return nil
	default:
		return tcpip.ErrConnectionRefused
	}
}

// ResolveStaticAddress attempts to resolve address without sending requests.
// It either resolves the name immediately or returns the empty LinkAddress.
func (r *entryTestLinkResolver) ResolveStaticAddress(addr tcpip.Address) (tcpip.LinkAddress, bool) {
	return "", false
}

// LinkAddressProtocol returns the network protocol of the addresses this
// resolver can resolve.
func (r *entryTestLinkResolver) LinkAddressProtocol() tcpip.NetworkProtocolNumber {
	return entryTestNetNumber
}

func expectProbes(t *testing.T, linkRes *entryTestLinkResolver, probes []entryTestProbeInfo) {
	for _, wantProbe := range probes {
		select {
		case gotProbe := <-linkRes.C:
			if got, want := gotProbe.RemoteAddress, wantProbe.RemoteAddress; got != want {
				t.Errorf("got RemoteAddress=%q, want=%q", string(got), string(want))
			}
			if got, want := gotProbe.RemoteLinkAddress, wantProbe.RemoteLinkAddress; got != want {
				t.Errorf("got RemoteLinkAddress=%q, want=%q", string(got), string(want))
			}
		case <-time.After(time.Second):
			t.Fatal("probe not sent within the last second")
		}
	}
	if len(linkRes.C) > 0 {
		t.Fatalf("expected no more probes sent, got %d more", len(linkRes.C))
	}
}

func expectEvents(t *testing.T, d *entryTestDispatcher, events []entryTestEventInfo) {
	for _, wantEvent := range events {
		select {
		case gotEvent := <-d.C:
			if got, want := gotEvent.eventType, wantEvent.eventType; got != want {
				t.Errorf("event.eventType=%q, want=%q", got, want)
			}
			if got, want := gotEvent.nicID, wantEvent.nicID; got != want {
				t.Errorf("event.nicID=%q, want=%q", got, want)
			}
			if got, want := gotEvent.addr, wantEvent.addr; got != want {
				t.Errorf("event.addr=%q, want=%q", got, want)
			}
			if got, want := gotEvent.linkAddr, wantEvent.linkAddr; got != want {
				t.Errorf("event.linkAddr=%q, want=%q", got, want)
			}
			if got, want := gotEvent.state, wantEvent.state; got != want {
				t.Errorf("event.state=%q, want=%q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s event not dispatched after one second", wantEvent.eventType)
		}
	}
	if len(d.C) > 0 {
		message := fmt.Sprintf("expect no more events dispatched, got %d more:", len(d.C))
		close(d.C)
		for e := range d.C {
			message += "\n" + e.String()
		}
		t.Fatal(message)
	}
}

// TestEntryInitiallyUnknown verifies that the state of a newly created
// neighborEntry is Unknown.
func TestEntryInitiallyUnknown(t *testing.T) {
	c := DefaultNUDConfigurations()
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Unknown; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{})
	expectEvents(t, &d, []entryTestEventInfo{})
}

func TestEntryUnknownToIncomplete(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration // only send one probe
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Incomplete; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})
	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
	})
}

func TestEntryUnknownToStale(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration // only send one probe
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handleProbeLocked(entryTestLinkAddr1)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{})
	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
	})
}

func TestEntryIncompleteToReachable(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration // only send one probe
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Incomplete; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleConfirmationLocked(entryTestLinkAddr1, true /* solicited */, false /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Reachable; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Reachable,
		},
	})
}

func TestEntryIncompleteToStale(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration // only send one probe
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Incomplete; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
	})
}

func TestEntryIncompleteToFailed(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = time.Microsecond
	c.MaxMulticastProbes = 3
	c.UnreachableTime = entryTestMaxDuration // don't transition out of Failed
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Incomplete; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		// The Incomplete-to-Incomplete state transition is tested here by
		// verifying that 3 reachability probes were sent.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestRemoved,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Failed; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()
}

func TestEntryReachableToStaleWhenTimeout(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = time.Microsecond
	c.MaxMulticastProbes = 1
	c.MaxUnicastProbes = 3
	c.BaseReachableTime = minimumBaseReachableTime
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, true /* solicited */, false /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Reachable; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Reachable,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()
}

func TestEntryReachableToStaleWhenProbeWithDifferentAddress(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration
	c.BaseReachableTime = entryTestMaxDuration // disable Reachable timer
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, true /* solicited */, false /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Reachable; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleProbeLocked(entryTestLinkAddr2)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Reachable,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr2,
			state:     Stale,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()
}

func TestEntryReachableToStaleWhenConfirmationWithDifferentAddress(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration
	c.BaseReachableTime = entryTestMaxDuration // disable Reachable timer
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, true /* solicited */, false /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Reachable; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleConfirmationLocked(entryTestLinkAddr2, false /* solicited */, true /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Reachable,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr2,
			state:     Stale,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()
}

func TestEntryStaleToDelay(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration     // only send one probe
	c.DelayFirstProbeTime = entryTestMaxDuration // only send one probe
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Delay; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
	})
}

func TestEntryDelayToReachable(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration     // only send one probe
	c.DelayFirstProbeTime = entryTestMaxDuration // stay in Delay
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Delay; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleUpperLevelConfirmationLocked()
	if got, want := e.mu.neigh.State, Reachable; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Reachable,
		},
	})
}

func TestEntryDelayToStaleWhenProbeWithDifferentAddress(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration     // only send one probe
	c.DelayFirstProbeTime = entryTestMaxDuration // stay in Delay
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Delay; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleProbeLocked(entryTestLinkAddr2)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr2,
			state:     Stale,
		},
	})
}

func TestEntryDelayToStaleWhenConfirmationWithDifferentAddress(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration     // only send one probe
	c.DelayFirstProbeTime = entryTestMaxDuration // stay in Delay
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Delay; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleConfirmationLocked(entryTestLinkAddr2, false /* solicited */, true /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr2,
			state:     Stale,
		},
	})
}

func TestEntryDelayToProbe(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration // only send one probe
	c.DelayFirstProbeTime = time.Microsecond
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	e.handlePacketQueuedLocked(&linkRes)
	if got, want := e.mu.neigh.State, Delay; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		// The first probe is caused by the Unknown-to-Incomplete transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
		// The second probe is caused by the Delay-to-Probe transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: entryTestLinkAddr1,
		},
	})

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Probe,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Probe; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()
}

func TestEntryProbeToStaleWhenProbeWithDifferentAddress(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration // only send one probe
	c.DelayFirstProbeTime = time.Microsecond // transition to Probe almost immediately
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	e.handlePacketQueuedLocked(&linkRes)
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		// The first probe is caused by the Unknown-to-Incomplete transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
		// The second probe is caused by the Delay-to-Probe transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: entryTestLinkAddr1,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Probe; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleProbeLocked(entryTestLinkAddr2)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Probe,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr2,
			state:     Stale,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()
}

func TestEntryProbeToStaleWhenConfirmationWithDifferentAddress(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration // only send one probe
	c.DelayFirstProbeTime = time.Microsecond // transition to Probe almost immediately
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	e.handlePacketQueuedLocked(&linkRes)
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		// The first probe is caused by the Unknown-to-Incomplete transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
		// The second probe is caused by the Delay-to-Probe transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: entryTestLinkAddr1,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Probe; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleConfirmationLocked(entryTestLinkAddr2, false /* solicited */, true /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Probe,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr2,
			state:     Stale,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Stale; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()
}

func TestEntryProbeToReachable(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = entryTestMaxDuration // only send one probe
	c.DelayFirstProbeTime = time.Microsecond // transition to Probe almost immediately
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	e.handlePacketQueuedLocked(&linkRes)
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		// The first probe is caused by the Unknown-to-Incomplete transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
		// The second probe is caused by the Delay-to-Probe transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: entryTestLinkAddr1,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Probe; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.handleConfirmationLocked(entryTestLinkAddr1, true /* solicited */, false /* override */, false /* isRouter */)
	if got, want := e.mu.neigh.State, Reachable; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Probe,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Reachable,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Reachable; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()
}

func TestEntryProbeToFailed(t *testing.T) {
	c := DefaultNUDConfigurations()
	c.RetransmitTimer = time.Microsecond // only send one probe
	c.MaxMulticastProbes = 3
	c.MaxUnicastProbes = 3
	c.DelayFirstProbeTime = time.Microsecond // transition to Probe almost immediately
	c.UnreachableTime = entryTestMaxDuration // don't transition out of Failed
	d := entryTestDispatcher{
		C: make(chan entryTestEventInfo, 300),
	}
	linkRes := entryTestLinkResolver{
		C: make(chan entryTestProbeInfo, 300),
	}
	e := newNeighborEntry(neighborEntryParams{
		nicID:      entryTestNICID,
		remoteAddr: entryTestAddr1,
		localAddr:  entryTestAddr2,
		nudState:   NewNUDState(c),
		dispatcher: &d,
		linkEP:     nil, // entryTestLinkResolver doesn't use a LinkEndpoint
		linkRes:    &linkRes,
	})

	e.mu.Lock()
	e.handlePacketQueuedLocked(&linkRes)
	e.handleConfirmationLocked(entryTestLinkAddr1, false /* solicited */, false /* override */, false /* isRouter */)
	e.handlePacketQueuedLocked(&linkRes)
	e.mu.Unlock()

	expectProbes(t, &linkRes, []entryTestProbeInfo{
		// The first probe is caused by the Unknown-to-Incomplete transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: tcpip.LinkAddress(""),
		},
		// The next three probe are caused by the Delay-to-Probe transition.
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: entryTestLinkAddr1,
		},
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: entryTestLinkAddr1,
		},
		{
			RemoteAddress:     entryTestAddr1,
			RemoteLinkAddress: entryTestLinkAddr1,
		},
	})
	expectEvents(t, &d, []entryTestEventInfo{
		{
			eventType: entryTestAdded,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  tcpip.LinkAddress(""),
			state:     Incomplete,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Stale,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Delay,
		},
		{
			eventType: entryTestStateChange,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Probe,
		},
		{
			eventType: entryTestRemoved,
			nicID:     entryTestNICID,
			addr:      entryTestAddr1,
			linkAddr:  entryTestLinkAddr1,
			state:     Probe,
		},
	})

	e.mu.Lock()
	if got, want := e.mu.neigh.State, Failed; got != want {
		t.Errorf("e.mu.neigh.State=%q, want=%q", got, want)
	}
	e.mu.Unlock()

}
