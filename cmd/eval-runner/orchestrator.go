package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

var (
	execLookPath        = exec.LookPath
	execCommandContext  = exec.CommandContext
)

type Orchestrator struct {
	ComposeFile string
	ProjectName string
}

func NewOrchestrator(composeFile, projectName string) *Orchestrator {
	return &Orchestrator{
		ComposeFile: composeFile,
		ProjectName: projectName,
	}
}

func (o *Orchestrator) Up(ctx context.Context) error {
	if _, err := execLookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH: %w", err)
	}

	output, err := o.runCompose(ctx, "up", "-d", "--build", "--wait")
	if err != nil {
		return fmt.Errorf("docker compose up failed: %s", tailLines(output, 20))
	}
	return nil
}

func (o *Orchestrator) Down(ctx context.Context) error {
	if _, err := execLookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH: %w", err)
	}

	if _, err := o.runCompose(ctx, "down", "-v"); err != nil {
		return fmt.Errorf("docker compose down failed: %w", err)
	}
	return nil
}

func (o *Orchestrator) runCompose(ctx context.Context, args ...string) (string, error) {
	cmdArgs := []string{"compose", "-f", o.ComposeFile, "-p", o.ProjectName}
	cmdArgs = append(cmdArgs, args...)

	cmd := execCommandContext(ctx, "docker", cmdArgs...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		return combined.String(), err
	}
	return combined.String(), nil
}

func tailLines(text string, count int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "(no compose output)"
	}
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}
