package blog

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminUIIndependentPages(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; HTML contracts still run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, filepath.Join("..", "..", "tests", "client", "test-admin-independent-pages-client.cjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("independent page UI contracts failed: %v\n%s", err, out)
	}
}
