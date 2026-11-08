package snapshot

import (
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

func readInto(s *domain.Store) func(io.Reader) error {
	return func(r io.Reader) error {
		_, err := s.ReadSnapshot(r)
		return err
	}
}

func TestFileRoundTrip(t *testing.T) {
	f := NewFile(filepath.Join(t.TempDir(), "sub", "kubeglance.snap"))
	if ok, err := f.Load(readInto(domain.NewStore(15*time.Second, time.Minute))); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	store := domain.NewStore(15*time.Second, time.Minute)
	key := domain.PodKey{Namespace: "a", Name: "b"}
	store.UpsertPods([]domain.PodInfo{{Key: key, Node: "n1"}})
	store.Record(domain.Round{At: time.Now().Truncate(time.Second), Pods: map[domain.PodKey]domain.Resource{key: {CPU: 42}}})
	if err := f.Save(store.WriteSnapshot); err != nil {
		t.Fatal(err)
	}
	dst := domain.NewStore(15*time.Second, time.Minute)
	if ok, err := f.Load(readInto(dst)); err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if info, ok := dst.Pod(key); !ok || info.Node != "n1" {
		t.Errorf("pod info = %+v", info)
	}
	if got := dst.PodSamples(key, time.Time{}); len(got) != 1 || got[0].Usage.CPU != 42 {
		t.Errorf("samples = %+v", got)
	}
	// overwrite is fine
	if err := f.Save(store.WriteSnapshot); err != nil {
		t.Fatal(err)
	}
}

func TestFileKeepsThePreviousSnapshotWhenAWriteFails(t *testing.T) {
	f := NewFile(filepath.Join(t.TempDir(), "kubeglance.snap"))
	store := domain.NewStore(15*time.Second, time.Minute)
	key := domain.PodKey{Namespace: "a", Name: "b"}
	store.Record(domain.Round{At: time.Now().Truncate(time.Second), Pods: map[domain.PodKey]domain.Resource{key: {CPU: 42}}})
	if err := f.Save(store.WriteSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(func(w io.Writer) error {
		_, _ = w.Write([]byte(`{"pods":[`))
		return errors.New("disk full")
	}); err == nil {
		t.Fatal("no error from a failing write")
	}
	dst := domain.NewStore(15*time.Second, time.Minute)
	if ok, err := f.Load(readInto(dst)); err != nil || !ok || len(dst.PodSamples(key, time.Time{})) != 1 {
		t.Errorf("previous snapshot lost: ok=%v err=%v", ok, err)
	}
}
