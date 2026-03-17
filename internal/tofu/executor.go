package tofu

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Executor struct {
	DataDir string
	Bin     string // path to tofu binary, defaults to "tofu"
}

func (e *Executor) bin() string {
	if e.Bin != "" {
		return e.Bin
	}
	return "tofu"
}

func (e *Executor) WorkspaceDir(workspaceID string) string {
	return filepath.Join(e.DataDir, "workspaces", workspaceID)
}

func (e *Executor) LogPath(workspaceID, buildID string) string {
	return filepath.Join(e.WorkspaceDir(workspaceID), "build-"+buildID+".log")
}

// WriteHCL writes the HCL content to main.tf in the workspace directory.
func (e *Executor) WriteHCL(workspaceID, hcl string) error {
	dir := e.WorkspaceDir(workspaceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "main.tf"), []byte(hcl), 0o644)
}

// Run executes an OpenTofu operation for the given workspace.
// transition is "start", "stop", or "delete".
// params is a list of "KEY=VALUE" strings passed as TF_VAR_KEY=VALUE.
// All output is written to logPath.
func (e *Executor) Run(ctx context.Context, workspaceID, transition string, params []string, logPath string) error {
	dir := e.WorkspaceDir(workspaceID)

	// Skip if workspace was never initialized (no main.tf) and transition is delete.
	mainTF := filepath.Join(dir, "main.tf")
	if _, err := os.Stat(mainTF); os.IsNotExist(err) {
		if transition == "delete" {
			return nil
		}
		return fmt.Errorf("workspace directory not initialized: main.tf missing")
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	defer logFile.Close()

	env := e.buildEnv(transition, params)

	fmt.Fprintf(logFile, "==> tofu init\n\n")
	if err := e.exec(ctx, dir, logFile, env, "init", "-no-color", "-input=false"); err != nil {
		fmt.Fprintf(logFile, "\n[error] tofu init failed: %v\n", err)
		return fmt.Errorf("tofu init: %w", err)
	}

	if transition == "delete" {
		fmt.Fprintf(logFile, "\n==> tofu destroy\n\n")
		if err := e.exec(ctx, dir, logFile, env, "destroy", "-auto-approve", "-no-color", "-input=false"); err != nil {
			fmt.Fprintf(logFile, "\n[error] tofu destroy failed: %v\n", err)
			return fmt.Errorf("tofu destroy: %w", err)
		}
	} else {
		fmt.Fprintf(logFile, "\n==> tofu apply\n\n")
		if err := e.exec(ctx, dir, logFile, env, "apply", "-auto-approve", "-no-color", "-input=false"); err != nil {
			fmt.Fprintf(logFile, "\n[error] tofu apply failed: %v\n", err)
			return fmt.Errorf("tofu apply: %w", err)
		}
	}

	return nil
}

func (e *Executor) buildEnv(transition string, params []string) []string {
	cacheDir := filepath.Join(e.DataDir, ".terraform-cache")
	os.MkdirAll(cacheDir, 0o755)

	env := []string{
		"TF_PLUGIN_CACHE_DIR=" + cacheDir,
		"TF_VAR_transition=" + transition,
	}
	for _, p := range params {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 && kv[0] != "" {
			env = append(env, "TF_VAR_"+kv[0]+"="+kv[1])
		}
	}
	return env
}

func (e *Executor) exec(ctx context.Context, dir string, out io.Writer, extraEnv []string, args ...string) error {
	cmd := exec.CommandContext(ctx, e.bin(), args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.Run()
}

// ReadLog returns the contents of a log file, or an empty string if it doesn't exist.
func ReadLog(logPath string) string {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	return string(data)
}
