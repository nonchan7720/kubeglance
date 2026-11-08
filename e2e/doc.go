// Package e2e holds the end-to-end tests: kubeglance on a kind cluster
// with metrics-server, two replicas and alerts to a receiver. They carry the
// e2e build tag and need the cluster scripts/e2e.sh sets up, so plain
// `go test ./...` skips them; run them with `mise run e2e`.
package e2e
