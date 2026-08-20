package tokenize

import (
	"bytes"
	"compress/gzip"
	"container/heap"
	"container/list"
	_ "embed"
	"encoding/base64"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Kind selects a BPE vocabulary family. Both kinds currently use the OpenAI
// cl100k_base merge ranks; exact Anthropic tokenization requires Claude's
// SentencePiece model, which is not vendored (provider-reported usage takes
// precedence in the ledger anyway). This is a documented approximation.
type Kind int

const (
	// KindOpenAI counts with the cl100k_base BPE merge ranks.
	KindOpenAI Kind = iota
	// KindAnthropic counts with the same cl100k ranks (approximation).
	KindAnthropic
)

//go:embed data/cl100k_base.bpe.txt.gz
var cl100kGz []byte

var (
	vocabOnce  sync.Once
	rank       map[string]uint16 // byte-string -> merge rank ("" on failure)
	vocabErr   error
	countStore = newCountCache(4096)
)

// loadVocab parses the embedded .tiktoken file once into a byte-string ->
// rank table. Each line is "<base64-bytes> <rank>".
func loadVocab() (map[string]uint16, error) {
	vocabOnce.Do(func() {
		m := map[string]uint16{}
		gz, err := gzip.NewReader(bytes.NewReader(cl100kGz))
		if err != nil {
			vocabErr = err
			return
		}
		data, err := io.ReadAll(gz)
		_ = gz.Close()
		if err != nil {
			vocabErr = err
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			sp := strings.IndexByte(line, ' ')
			if sp < 0 {
				continue
			}
			r, err := strconv.Atoi(line[sp+1:])
			if err != nil {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(line[:sp])
			if err != nil {
				continue
			}
			m[string(raw)] = uint16(r)
		}
		if len(m) == 0 {
			vocabErr = io.ErrUnexpectedEOF
			return
		}
		rank = m
	})
	return rank, vocabErr
}

// Count returns the BPE token count for text under kind. If the embedded
// vocab is missing/corrupt, it falls back to Estimate (fail-open). Results
// are cached per (text-hash, kind).
func Count(text string, kind Kind) int {
	if text == "" {
		return 0
	}
	if n, ok := countStore.get(kind, text); ok {
		return n
	}
	r, err := loadVocab()
	if err != nil {
		return Estimate(text)
	}
	n := countBPE(r, text)
	countStore.put(kind, text, n)
	return n
}

// countBPE performs the standard greedy byte-pair merge over the text and
// returns the resulting token count. It uses a min-heap of adjacent-pair
// ranks (the tiktoken algorithm) so the merge runs in O(n log n) instead of
// the naive O(n^2), which is what makes counting multi-hundred-KB payloads
// fast enough for the live clamp and the bench gate.
func countBPE(r map[string]uint16, text string) int {
	n := len(text)
	if n == 0 {
		return 0
	}
	// Each token is a byte range [start,end) into text, linked via next[] and
	// prev[] (doubly-linked so both neighbors' ranks are recomputed on merge).
	starts := make([]int, n)
	ends := make([]int, n)
	prev := make([]int, n)
	next := make([]int, n)
	for i := 0; i < n; i++ {
		starts[i], ends[i], next[i] = i, i+1, i+1
		prev[i] = i - 1
	}
	next[n-1] = -1

	// rankOf returns the merge rank of adjacent tokens i and next[i].
	rankOf := func(i int) (uint16, bool) {
		j := next[i]
		if j < 0 {
			return 0, false
		}
		key := string(text[starts[i]:ends[i]]) + string(text[starts[j]:ends[j]])
		rr, ok := r[key]
		return rr, ok
	}

	count := n
	valid := make([]bool, n) // adjacency starting at i is live (i and next[i] exist)
	version := make([]int, n)
	var h pairHeap
	push := func(i int, rank uint16) {
		version[i]++
		heap.Push(&h, adj{rank: rank, idx: i, ver: version[i]})
	}
	recompute := func(i int) {
		if i < 0 || i >= n {
			return
		}
		if next[i] < 0 {
			valid[i] = false
			return
		}
		if rr, ok := rankOf(i); ok {
			valid[i] = true
			push(i, rr)
		} else {
			valid[i] = false
		}
	}
	// Seed the heap with every initial adjacent pair that has a rank.
	for i := 0; i < n-1; i++ {
		recompute(i)
	}

	for h.Len() > 0 {
		a := heap.Pop(&h).(adj)
		i := a.idx
		if !valid[i] || a.ver != version[i] {
			continue // stale
		}
		j := next[i]
		if j < 0 {
			continue
		}
		// Merge token i into token j: extend i's range, drop j from the list.
		ends[i] = ends[j]
		next[i] = next[j]
		if next[i] >= 0 {
			prev[next[i]] = i
		}
		valid[j] = false
		count--

		// Token i's bytes changed, so recompute the ranks of the two
		// adjacencies that touch it: (prev[i], i) and (i, next[i]).
		recompute(prev[i])
		recompute(i)
	}
	return count
}

// adj is one heap entry: the merge rank of the adjacent pair starting at
// idx, tagged with a version for stale-entry detection.
type adj struct {
	rank uint16
	idx  int
	ver  int
}

// pairHeap is a min-heap of adjacencies ordered by rank then index.
type pairHeap []adj

func (h pairHeap) Len() int { return len(h) }
func (h pairHeap) Less(i, j int) bool {
	return h[i].rank < h[j].rank || (h[i].rank == h[j].rank && h[i].idx < h[j].idx)
}
func (h pairHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *pairHeap) Push(x any)   { *h = append(*h, x.(adj)) }
func (h *pairHeap) Pop() any     { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

// ---- bounded LRU cache for Count ----

type countKey struct {
	kind Kind
	hash uint64
}

type countEntry struct {
	key countKey
	n   int
}

// countCache is a tiny mutex-guarded LRU (keyed by FNV hash + kind). It is
// approximate: hash collisions are possible, but a wrong count from a
// collision is harmless for a measurement instrument.
type countCache struct {
	mu   sync.Mutex
	cap  int
	lru  *list.List // front = most recent
	byID map[countKey]*list.Element
}

func newCountCache(capacity int) *countCache {
	return &countCache{cap: capacity, lru: list.New(), byID: map[countKey]*list.Element{}}
}

func (c *countCache) hash(kind Kind, s string) countKey {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return countKey{kind: kind, hash: h}
}

func (c *countCache) get(kind Kind, s string) (int, bool) {
	k := c.hash(kind, s)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byID[k]; ok {
		c.lru.MoveToFront(el)
		return el.Value.(countEntry).n, true
	}
	return 0, false
}

func (c *countCache) put(kind Kind, s string, n int) {
	k := c.hash(kind, s)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byID[k]; ok {
		el.Value = countEntry{key: k, n: n}
		c.lru.MoveToFront(el)
		return
	}
	c.byID[k] = c.lru.PushFront(countEntry{key: k, n: n})
	if c.lru.Len() > c.cap {
		back := c.lru.Back()
		if back != nil {
			c.lru.Remove(back)
			delete(c.byID, back.Value.(countEntry).key)
		}
	}
}
