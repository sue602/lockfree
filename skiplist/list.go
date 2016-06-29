package skiplist

import (
	"math"
	"sync"
	"sync/atomic"
	"unsafe"
)

//Header of a skip list
type Header struct {
	length                      uint32
	leftSentinel, rightSentinel *Node
}

//Node of a skip list
type Node struct {
	key         int
	value       unsafe.Pointer //user stuff
	nexts       nodeSlice      // slice of *Node
	marked      bool
	fullyLinked bool
	lock        sync.Mutex
}

type nodeSlice []unsafe.Pointer // atomic slice of *Node
// type nodeSlice []*Node

func newFullNodeSlice() nodeSlice {
	var slice [maxlevel]unsafe.Pointer
	// var slice [maxlevel]*Node
	return slice[:]
}
func (ns nodeSlice) get(layer int) *Node {
	return (*Node)(atomic.LoadPointer(&ns[layer]))
	// return ns[layer]
}
func (ns nodeSlice) set(layer int, n *Node) {
	atomic.StorePointer(&ns[layer], unsafe.Pointer(n))
	// ns[layer] = n
}
func (ns nodeSlice) unlock(highest int) {
	var prev *Node
	for i := highest; i >= 0; i-- {
		curr := ns.get(i)
		if curr != prev {
			curr.lock.Unlock()
			prev = curr
		}
	}
}

//New valid skiplist !
func New() *Header {
	h := &Header{}
	h.Initialize()
	return h
}

// Initialize resets the list to a default empty state
func (h *Header) Initialize() {
	left := newFullNodeSlice()
	right := newFullNodeSlice()
	rightMost := &Node{
		key:         int(math.MaxInt32),
		nexts:       right[:],
		fullyLinked: true,
	}
	for i := range left {
		left.set(i, rightMost)
	}
	leftMost := &Node{
		key:         int(math.MinInt32),
		nexts:       left[:],
		fullyLinked: true,
	}

	h.leftSentinel, h.rightSentinel = leftMost, rightMost
}

func (n *Node) contains(v int) bool {
	return n.key == v
}
func (n *Node) lowerThan(v int) bool {
	return n.key < v
}

//findNode searches for every node that are or could be directly linked to v
//before & after for every layer
//
////returns -1 if v was not found
//returns the layer at wich the node could be found
//
//Ex:
//
// searching for 0, 1, 2 or 3
// [n] == preds
// (n) == succs
//
// [-∞] -------------------------------------> +∞ | maxlevel
//  -∞ -> -3 -> -2 -> [-1] ------------------> +∞ | maxlevel - 1
//  -∞ -> -3 -> -2 -> [-1] ------------------> +∞ | maxlevel - 2
//  -∞ -> -3 -> -2 -> [-1] -> (3) ------> 9 -> +∞ | maxlevel - 3
//  -∞ -> -3 -> -2 -> [-1] -> (3) ------> 9 -> +∞ | maxlevel - 3
//  -∞ -> -3 -> -2 -> [-1] -> (3) -> 6 -> 9 -> +∞ | maxlevel - 4
//  -∞ -> -3 -> -2 -> [-1] -> (3) -> 6 -> 9 -> +∞ | 0
func (h *Header) findNode(v int, preds, succs nodeSlice) (lFound int) {
	lFound = -1
	left := h.leftSentinel
	for layer := maxlevel - 1; layer >= 0; layer-- {
		right := left.nexts.get(layer)
		for right.lowerThan(v) {
			left = right
			right = left.nexts.get(layer)
		}
		if lFound == -1 && right.contains(v) {
			lFound = layer
		}
		preds.set(layer, left)
		succs.set(layer, right)
	}

	return
}

//Set adds ptr into list at v
//returns false if it was just an edit
//returns true if it was added
func (h *Header) Set(v int, ptr unsafe.Pointer) bool {
	topLayer := generateLevel(maxlevel)
	preds, succs := newFullNodeSlice(), newFullNodeSlice()
	for {
		lFound := h.findNode(v, preds, succs)
		if lFound != -1 { // node was found
			nodeFound := succs.get(lFound)
			if !nodeFound.marked {
				for !nodeFound.fullyLinked {
					//make sure everything is valid
				}
				//node already in there
				atomic.StorePointer(&nodeFound.value, ptr)
				return false
			}
			//something is deleting that node
			//let's try again
			continue
		}
		highestLocked := -1

		var prevPred, pred, succ *Node
		valid := true
		for layer := 0; valid && layer <= topLayer; layer++ {
			pred = preds.get(layer)
			succ = succs.get(layer)
			if pred != prevPred {
				pred.lock.Lock()
				highestLocked = layer
				prevPred = pred
			}
			valid = !pred.marked && !succ.marked && pred.nexts.get(layer) == succ
		}
		if !valid {
			preds.unlock(highestLocked)
			continue
		}
		newNode := newNode(ptr, v, topLayer)
		for layer := 0; layer <= topLayer; layer++ {
			newNode.nexts.set(layer, succs.get(layer))
			preds.get(layer).nexts.set(layer, newNode)
		}
		newNode.fullyLinked = true
		preds.unlock(highestLocked)
		atomic.AddUint32(&h.length, 1)
		return true
	}
}

//Remove node containing v if any
//return false if a remove is already in progress on that node
func (h *Header) Remove(v int) bool {
	var nodeToDelete *Node
	isMarked := false
	topLayer := -1
	preds, succs := newFullNodeSlice(), newFullNodeSlice()
	for {
		lFound := h.findNode(v, preds, succs)
		if !(isMarked || (lFound != -1 && succs.get(lFound).okToDelete(lFound))) {
			return false
		}
		if !isMarked {
			nodeToDelete = succs.get(lFound)
			topLayer = len(nodeToDelete.nexts) - 1
			nodeToDelete.lock.Lock()
			if nodeToDelete.marked {
				nodeToDelete.lock.Unlock()
				return false
			}
			nodeToDelete.marked = true
			isMarked = true
		}
		highestLocked := -1

		var prevPred, pred, succ *Node
		valid := true
		for layer := 0; valid && (layer <= topLayer); layer++ {
			pred = preds.get(layer)
			succ = succs.get(layer)
			if pred != prevPred {
				pred.lock.Lock()
				highestLocked = layer
				prevPred = pred
			}
			valid = !pred.marked && pred.nexts.get(layer) == succ
		}
		if !valid {
			preds.unlock(highestLocked)
			continue
		}
		for layer := topLayer; layer >= 0; layer-- {
			preds.get(layer).nexts.set(layer, nodeToDelete.nexts.get(layer))
		}
		nodeToDelete.lock.Unlock()
		preds.unlock(highestLocked)
		atomic.AddUint32(&h.length, ^uint32(0))
		return true
	}
}

func (n *Node) okToDelete(lFound int) bool {
	return (n.fullyLinked) && len(n.nexts) == lFound+1 && !n.marked
}

//Contains returns true if v was found in list
func (h *Header) Contains(v int) bool {
	preds, succs := newFullNodeSlice(), newFullNodeSlice()
	lFound := h.findNode(v, preds, succs)
	return lFound != -1 && succs.get(lFound).fullyLinked && !succs.get(lFound).marked
}

//Get returns (ptr, true) if something was found, (nil, false) otherwise
func (h *Header) Get(v int) (ptr unsafe.Pointer, found bool) {
	preds, succs := newFullNodeSlice(), newFullNodeSlice()
	lFound := h.findNode(v, preds, succs)

	if lFound == -1 {
		return nil, false
	}
	n := succs.get(lFound)
	if !n.fullyLinked || n.marked {
		return nil, false
	}
	return atomic.LoadPointer(&n.value), true
}

//newNode instanciates a *Node with topLayer set right
// and a slice of `topLayer` sized nexts
func newNode(ptr unsafe.Pointer, v, topLayer int) *Node {
	n := &Node{
		value: ptr,
		key:   v,
		nexts: make([]unsafe.Pointer, topLayer+1),
		// nexts: make([]*Node, topLayer+1),
	}
	// n.lock.Lock()
	return n
}

//Len returns the size of the list
func (h *Header) Len() int {
	return int(atomic.LoadUint32(&h.length))
}
