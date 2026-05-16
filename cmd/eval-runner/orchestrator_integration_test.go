package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOrchestratorIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Docker integration test in short mode")
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}

	composeFile := "testdata/orchestrator/integration-compose.yml"
	projectName := fmt.Sprintf("evalrunneritest%d", time.Now().UnixNano())
	orch := NewOrchestrator(composeFile, projectName)

	if err := orch.Up(ctx); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	defer func() {
		if err := orch.Down(context.Background()); err != nil {
			t.Fatalf("Down() error = %v", err)
		}
	}()

	services, err := dockerComposeOutput(ctx, composeFile, projectName, "ps", "--services", "--filter", "status=running")
	if err != nil {
		t.Fatalf("docker compose ps error = %v", err)
	}
	if !strings.Contains(services, "healthy") {
		t.Fatalf("running services output = %q, want healthy service name", services)
	}
}

func dockerComposeOutput(ctx context.Context, composeFile, projectName string, args ...string) (string, error) {
	cmdArgs := []string{"compose", "-f", composeFile, "-p", projectName}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		return combined.String(), err
	}
	return combined.String(), nil
}
