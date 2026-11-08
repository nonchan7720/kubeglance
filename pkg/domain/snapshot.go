package domain

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"
)

// snapshotHead is a Snapshot without its pods and nodes, with the same JSON
// field names, in the same order.
type snapshotHead struct {
	TakenAt   time.Time     `json:"takenAt"`
	Interval  time.Duration `json:"interval"`
	Timeline  Timeline      `json:"timeline,omitempty"`
	Retention time.Duration `json:"retention"`
	Newest    time.Time     `json:"newest,omitzero"`
}

// SnapshotInfo describes a snapshot ReadSnapshot merged.
type SnapshotInfo struct {
	TakenAt time.Time
	Pods    int
	Nodes   int
}

// WriteSnapshot writes the store as the JSON encoding of a Snapshot, one pod
// or node at a time: each is copied under the read lock and written before
// the next is copied. The history is never encoded whole in memory, which
// would cost several times the history itself (JSON text is ~5 times the
// stored samples) on the replica that is serving it, during a rolling
// update, while it keeps collecting. An entity removed meanwhile is left
// out; each one is consistent in itself, which is all Merge needs.
//
// Samples recorded after the header was taken (newer than its "newest") are
// left out, as if the snapshot had been taken at once: they may sit on a
// grid the header's timeline does not describe yet (the interval changed
// meanwhile), and the reader slots by that timeline.
func (s *Store) WriteSnapshot(w io.Writer) error {
	s.mu.RLock()
	head := s.head()
	pods := slices.Collect(maps.Keys(s.pods))
	nodes := slices.Collect(maps.Keys(s.nodes))
	s.mu.RUnlock()
	until := head.Newest
	if until.IsZero() { // no samples yet: none recorded later either
		until = time.Unix(0, 0)
	}

	bw := bufio.NewWriter(w)
	hb, err := json.Marshal(head)
	if err != nil {
		return err
	}
	if _, err := bw.Write(hb[:len(hb)-1]); err != nil { // without the closing }
		return err
	}
	if err := writeList(bw, `,"pods":[`, pods, func(k PodKey) (any, bool) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if e, ok := s.pods[k]; ok {
			return podSnapshot(e, until), true
		}
		return nil, false
	}); err != nil {
		return err
	}
	if err := writeList(bw, `],"nodes":[`, nodes, func(n string) (any, bool) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if e, ok := s.nodes[n]; ok {
			return nodeSnapshot(e, until), true
		}
		return nil, false
	}); err != nil {
		return err
	}
	if _, err := bw.WriteString("]}\n"); err != nil {
		return err
	}
	return bw.Flush()
}

// writeList writes open, then the JSON of every key's value, comma separated.
func writeList[K any](w *bufio.Writer, open string, keys []K, value func(K) (any, bool)) error {
	if _, err := w.WriteString(open); err != nil {
		return err
	}
	first := true
	for _, k := range keys {
		v, ok := value(k)
		if !ok {
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if !first {
			if err := w.WriteByte(','); err != nil {
				return err
			}
		}
		first = false
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// ReadSnapshot merges a snapshot (the JSON WriteSnapshot writes, or the
// encoding of a Snapshot by a replica that predates it) as it reads: one
// pod or node is decoded and merged before the next is read, so a starting
// replica holds one entity of the incoming history at a time, not all of it
// twice. The timeline is merged before the first pod, from the fields
// before it (every version writes them first). A snapshot without "newest"
// places the store's current interval after takenAt instead.
//
// On an error, what was read so far stays merged; merging is idempotent,
// so reading the snapshot again (or another peer's) completes it.
func (s *Store) ReadSnapshot(r io.Reader) (SnapshotInfo, error) {
	var info SnapshotInfo
	dec := json.NewDecoder(r)
	if err := expect(dec, '{'); err != nil {
		return info, err
	}
	var head snapshotHead
	headMerged := false
	mergeHead := func() {
		if headMerged {
			return
		}
		headMerged = true
		last := head.Newest
		if last.IsZero() {
			last = head.TakenAt
		}
		s.mergeTimeline(head.Timeline, head.Interval, last)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return info, err
		}
		var n int
		if headMerged && isHeader(tok) {
			return info, fmt.Errorf("snapshot %v after its samples", tok)
		}
		switch tok {
		case "takenAt":
			err = dec.Decode(&head.TakenAt)
		case "interval":
			err = dec.Decode(&head.Interval)
		case "timeline":
			err = dec.Decode(&head.Timeline)
		case "retention":
			err = dec.Decode(&head.Retention)
		case "newest":
			err = dec.Decode(&head.Newest)
		case "pods":
			mergeHead()
			n, err = eachElement(dec, func() error {
				var p PodSnapshot
				if err := dec.Decode(&p); err != nil {
					return err
				}
				s.mergePod(p)
				return nil
			})
			info.Pods += n
		case "nodes":
			mergeHead()
			n, err = eachElement(dec, func() error {
				var x NodeSnapshot
				if err := dec.Decode(&x); err != nil {
					return err
				}
				s.mergeNode(x)
				return nil
			})
			info.Nodes += n
		default: // a field of a later version
			var skip json.RawMessage
			err = dec.Decode(&skip)
		}
		if err != nil {
			return info, fmt.Errorf("snapshot %v: %w", tok, err)
		}
	}
	if err := expect(dec, '}'); err != nil {
		return info, err
	}
	mergeHead()
	info.TakenAt = head.TakenAt
	return info, nil
}

// isHeader reports whether a snapshot member is part of the header, which
// every writer puts before the samples it is needed to slot.
func isHeader(key json.Token) bool {
	switch key {
	case "takenAt", "interval", "timeline", "retention", "newest":
		return true
	}
	return false
}

// eachElement reads a JSON array (or null), calling each with the decoder
// on every element; each must consume it.
func eachElement(dec *json.Decoder, each func() error) (int, error) {
	tok, err := dec.Token()
	if err != nil || tok == nil {
		return 0, err
	}
	if tok != json.Delim('[') {
		return 0, fmt.Errorf("want an array, got %v", tok)
	}
	n := 0
	for dec.More() {
		if err := each(); err != nil {
			return n, err
		}
		n++
	}
	return n, expect(dec, ']')
}

// expect reads the delimiter d; the end of the input is an unexpected EOF.
func expect(dec *json.Decoder, d json.Delim) error {
	tok, err := dec.Token()
	if err == nil && tok != d {
		err = fmt.Errorf("want %v, got %v", d, tok)
	}
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return err
}
