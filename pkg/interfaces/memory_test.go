package interfaces

import "testing"

func TestGoMemoryLimit(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want int64
		ok   bool
	}{
		{"the container's limit: 90 % of it", map[string]string{"KUBEGLANCE_MEMORY_LIMIT": "134217728"}, 120795955, true},
		{"GOMEMLIMIT set by hand wins (the runtime applied it)", map[string]string{"KUBEGLANCE_MEMORY_LIMIT": "134217728", "GOMEMLIMIT": "100MiB"}, 0, false},
		{"no limit known", map[string]string{}, 0, false},
		{"garbage", map[string]string{"KUBEGLANCE_MEMORY_LIMIT": "128Mi"}, 0, false},
		{"zero", map[string]string{"KUBEGLANCE_MEMORY_LIMIT": "0"}, 0, false},
	} {
		got, ok := GoMemoryLimit(func(k string) string { return c.env[k] })
		if got != c.want || ok != c.ok {
			t.Errorf("%s: GoMemoryLimit = %d, %v; want %d, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
