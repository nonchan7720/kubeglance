package snapshot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

func TestFileRoundTrip(t *testing.T) {
	f := NewFile(filepath.Join(t.TempDir(), "sub", "kubeglance.snap"))
	if _, ok, err := f.Load(); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	store := domain.NewStore(15*time.Second, time.Minute)
	key := domain.PodKey{Namespace: "a", Name: "b"}
	store.UpsertPods([]domain.PodInfo{{Key: key, Node: "n1"}})
	store.Record(domain.Round{At: time.Now().Truncate(time.Second), Pods: map[domain.PodKey]domain.Resource{key: {CPU: 42}}})
	if err := f.Save(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	snap, ok, err := f.Load()
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if len(snap.Pods) != 1 || snap.Pods[0].Info.Node != "n1" || snap.Pods[0].Samples[0].Usage.CPU != 42 {
		t.Errorf("snapshot = %+v", snap)
	}
	// overwrite is fine
	if err := f.Save(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
}
