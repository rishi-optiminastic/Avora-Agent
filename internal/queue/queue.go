// Package queue is the agent's durable offline buffer: activity samples and
// screenshots that could not be delivered are written here and sent later,
// oldest first, so a laptop that loses its connection (a train, a hotel wifi
// login page, a backend redeploy) loses nothing it captured.
//
// One entry is one JSON file in the queue directory, named
// "<id>-<capturedUnixNano>.json". The id is a per-queue counter, so sorting by
// name is enqueue order, and the capture time lives in the name so the index
// can be rebuilt on start without reading (possibly large) screenshot files.
//
// Every write is atomic (temp file + rename), so a crash or kill can leave a
// stray temp file but never a half-written entry. The queue is bounded by item
// count, total bytes and age; when full it drops the OLDEST entries, because
// the newest are both the most valuable and the least distorted by delay.
package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"avora-agent/internal/capture"
	"avora-agent/internal/collect"
)

// Entry kinds.
const (
	KindActivity   = "activity"
	KindScreenshot = "screenshot"
)

const (
	entryExt = ".json"
	tmpExt   = ".tmp"
	idDigits = 20 // zero-padded so lexical order is numeric order
)

// Entry is one undelivered item.
type Entry struct {
	// ID is assigned by Push; empty for an entry that was never queued.
	ID         string    `json:"-"`
	Kind       string    `json:"kind"`
	CapturedAt time.Time `json:"captured_at"`

	// Sequence is pinned the first time an activity sample is sent and reused
	// on every retry, so a sample the server accepted on an attempt whose reply
	// was lost is recognised as a replay (409) instead of stored twice.
	Sequence int `json:"sequence,omitempty"`
	// SeqSent is true once a request carrying Sequence may have been committed
	// by the server (the reply was lost, or the server failed after receiving
	// it). Only then does a 409 mean "already accepted".
	SeqSent bool `json:"seq_sent,omitempty"`
	// Attempts counts failed delivery attempts, for the log.
	Attempts int `json:"attempts,omitempty"`

	Sample *collect.Sample `json:"sample,omitempty"`
	Shot   *capture.Shot   `json:"shot,omitempty"`
}

// Limits bound the queue on disk.
type Limits struct {
	MaxItems int
	MaxBytes int64
	MaxAge   time.Duration
	// MaxAgeByKind overrides MaxAge for one entry kind (e.g. activity kept
	// only briefly, screenshots longer).
	MaxAgeByKind map[string]time.Duration
}

// maxAge is how long an entry of kind may wait; zero means no limit.
func (l Limits) maxAge(kind string) time.Duration {
	if age, ok := l.MaxAgeByKind[kind]; ok {
		return age
	}
	return l.MaxAge
}

// Stats is a snapshot of what is waiting.
type Stats struct {
	Items int
	Bytes int64
	// OldestAge is how long ago the oldest waiting entry was captured; zero
	// when the queue is empty.
	OldestAge time.Duration
}

type indexEntry struct {
	id         string
	file       string
	kind       string // empty for entries written before the kind was in the name
	size       int64
	capturedAt time.Time
}

// Queue is a bounded, persistent FIFO. It is safe for concurrent use.
type Queue struct {
	mu     sync.Mutex
	dir    string
	limits Limits
	index  []indexEntry // oldest first
	nextID uint64
	logf   func(format string, args ...any)
}

// ErrTooLarge means a single entry exceeds the whole queue's byte budget.
var ErrTooLarge = errors.New("entry larger than the queue's byte limit")

// Open loads (or creates) the queue in dir. Leftover temp files and entries
// whose names cannot be parsed are removed. Entries are not read here, so
// opening stays cheap however much is waiting.
func Open(dir string, limits Limits, logf func(format string, args ...any)) (*Queue, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	q := &Queue{dir: dir, limits: limits, logf: logf, nextID: 1}
	for _, d := range names {
		if d.IsDir() {
			continue
		}
		name := d.Name()
		full := filepath.Join(dir, name)
		ie, ok := parseName(name)
		info, statErr := d.Info()
		if !ok || statErr != nil {
			_ = os.Remove(full) // temp leftover from a crash, or not ours
			continue
		}
		ie.size = info.Size()
		q.index = append(q.index, ie)
	}
	sort.Slice(q.index, func(i, j int) bool { return q.index[i].id < q.index[j].id })
	if n := len(q.index); n > 0 {
		last, _ := strconv.ParseUint(q.index[n-1].id, 10, 64)
		q.nextID = last + 1
	}
	return q, nil
}

func parseName(name string) (indexEntry, bool) {
	if !strings.HasSuffix(name, entryExt) {
		return indexEntry{}, false
	}
	stem := strings.TrimSuffix(name, entryExt)
	id, rest, ok := strings.Cut(stem, "-")
	if !ok || len(id) != idDigits {
		return indexEntry{}, false
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return indexEntry{}, false
	}
	// "<nanos>-<kind>"; older builds wrote just "<nanos>".
	nanos, kind, _ := strings.Cut(rest, "-")
	ns, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return indexEntry{}, false
	}
	return indexEntry{id: id, file: name, kind: kind, capturedAt: time.Unix(0, ns)}, true
}

// Push appends e (assigning e.ID) and then enforces the limits, dropping the
// oldest entries if the queue is over budget.
func (q *Queue) Push(e *Entry) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	id := fmt.Sprintf("%0*d", idDigits, q.nextID)
	name := fmt.Sprintf("%s-%d-%s%s", id, e.CapturedAt.UnixNano(), e.Kind, entryExt)
	size, err := q.write(name, e)
	if err != nil {
		return err
	}
	if q.limits.MaxBytes > 0 && size > q.limits.MaxBytes {
		_ = os.Remove(filepath.Join(q.dir, name))
		return ErrTooLarge
	}
	q.nextID++
	e.ID = id
	q.index = append(q.index, indexEntry{
		id: id, file: name, kind: e.Kind, size: size, capturedAt: e.CapturedAt,
	})
	q.enforceLimitsLocked()
	return nil
}

// Update rewrites a queued entry in place (e.g. after pinning its sequence).
func (q *Queue) Update(e *Entry) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.findLocked(e.ID)
	if i < 0 {
		return os.ErrNotExist
	}
	size, err := q.write(q.index[i].file, e)
	if err != nil {
		return err
	}
	q.index[i].size = size
	return nil
}

// Peek returns the oldest entry without removing it, or nil when empty. An
// entry that cannot be read back is dropped (and logged) and the next one is
// tried, so one damaged file can never wedge the queue.
func (q *Queue) Peek() *Entry {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.index) > 0 {
		head := q.index[0]
		e, err := q.read(head.file)
		if err == nil {
			e.ID = head.id
			return e
		}
		q.logf("  warn (queue): dropping unreadable entry %s: %v", head.file, err)
		q.removeAtLocked(0)
	}
	return nil
}

// Remove deletes the entry with this id (a no-op if it is already gone).
func (q *Queue) Remove(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if i := q.findLocked(id); i >= 0 {
		q.removeAtLocked(i)
	}
}

// PruneExpired drops entries captured more than MaxAge before now and returns
// how many were dropped.
func (q *Queue) PruneExpired(now time.Time) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	dropped := 0
	// Each kind has its own age limit and enqueue order is only close to
	// capture order, so sweep every entry.
	for i := 0; i < len(q.index); {
		limit := q.limits.maxAge(q.index[i].kind)
		if limit > 0 && now.Sub(q.index[i].capturedAt) > limit {
			q.removeAtLocked(i)
			dropped++
			continue
		}
		i++
	}
	if dropped > 0 {
		q.logf("  queue: dropped %d expired entries", dropped)
	}
	return dropped
}

// Clear removes everything (used when the device is re-enrolled: queued data
// belongs to the previous enrollment).
func (q *Queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.index) > 0 {
		q.removeAtLocked(0)
	}
}

// Len is the number of waiting entries.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.index)
}

// Stats summarises the queue as of now.
func (q *Queue) Stats(now time.Time) Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := Stats{Items: len(q.index), Bytes: q.bytesLocked()}
	for _, ie := range q.index {
		if age := now.Sub(ie.capturedAt); age > st.OldestAge {
			st.OldestAge = age
		}
	}
	return st
}

func (q *Queue) enforceLimitsLocked() {
	dropped := 0
	for len(q.index) > 0 && q.overLimitLocked() {
		q.removeAtLocked(0)
		dropped++
	}
	if dropped > 0 {
		q.logf("  warn (queue): full, dropped the %d oldest entries", dropped)
	}
}

func (q *Queue) overLimitLocked() bool {
	if q.limits.MaxItems > 0 && len(q.index) > q.limits.MaxItems {
		return true
	}
	return q.limits.MaxBytes > 0 && q.bytesLocked() > q.limits.MaxBytes
}

func (q *Queue) bytesLocked() int64 {
	var total int64
	for _, ie := range q.index {
		total += ie.size
	}
	return total
}

func (q *Queue) findLocked(id string) int {
	for i, ie := range q.index {
		if ie.id == id {
			return i
		}
	}
	return -1
}

func (q *Queue) removeAtLocked(i int) {
	_ = os.Remove(filepath.Join(q.dir, q.index[i].file))
	q.index = append(q.index[:i], q.index[i+1:]...)
}

// write stores e atomically under name and returns the size written.
func (q *Queue) write(name string, e *Entry) (int64, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	final := filepath.Join(q.dir, name)
	tmp := final + tmpExt
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return int64(len(data)), nil
}

func (q *Queue) read(name string) (*Entry, error) {
	data, err := os.ReadFile(filepath.Join(q.dir, name))
	if err != nil {
		return nil, err
	}
	e := &Entry{}
	if err := json.Unmarshal(data, e); err != nil {
		return nil, err
	}
	if e.Kind != KindActivity && e.Kind != KindScreenshot {
		return nil, fmt.Errorf("unknown kind %q", e.Kind)
	}
	return e, nil
}
