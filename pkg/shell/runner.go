package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultMaxStdin  = 1 << 20
	DefaultMaxStdout = 4 << 20
	DefaultMaxStderr = 64 << 10
	DefaultTimeout   = 30 * time.Second
)

const encodingPrelude = `$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)

`

type TrustedScript struct {
	Name    string
	Content string
}

type Runner struct {
	PwshPath  string
	TempDir   string
	MaxStdin  int64
	MaxStdout int64
	MaxStderr int64
	Timeout   time.Duration
}

func NewRunner() *Runner {
	return &Runner{
		PwshPath:  "pwsh",
		MaxStdin:  DefaultMaxStdin,
		MaxStdout: DefaultMaxStdout,
		MaxStderr: DefaultMaxStderr,
		Timeout:   DefaultTimeout,
	}
}

func (r *Runner) Run(ctx context.Context, trustedScript TrustedScript, requestJSON []byte) ([]byte, error) {
	cfg := r.withDefaults()
	if int64(len(requestJSON)) > cfg.MaxStdin {
		return nil, fmt.Errorf("PowerShell request exceeds stdin limit of %d bytes", cfg.MaxStdin)
	}
	if strings.TrimSpace(trustedScript.Content) == "" {
		return nil, errors.New("trusted PowerShell script is empty")
	}

	scriptPath, cleanup, err := cfg.writeTrustedScript(trustedScript)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	runCtx := ctx
	var cancel context.CancelFunc
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(
		runCtx,
		cfg.PwshPath,
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-File",
		scriptPath,
	)
	hideWindow(cmd)
	cmd.Stdin = bytes.NewReader(requestJSON)

	stdout := newBoundedBuffer(cfg.MaxStdout)
	stderr := newBoundedBuffer(cfg.MaxStderr)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	if runCtx.Err() != nil {
		return nil, fmt.Errorf("PowerShell request exceeded timeout of %s", cfg.Timeout)
	}
	if stdout.overflowed {
		return nil, fmt.Errorf("PowerShell stdout exceeded limit of %d bytes", cfg.MaxStdout)
	}
	if stderr.overflowed {
		return nil, fmt.Errorf("PowerShell stderr exceeded limit of %d bytes", cfg.MaxStderr)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("PowerShell bridge failed: %s", message)
	}

	return bytes.TrimSpace(stdout.Bytes()), nil
}

func (r *Runner) withDefaults() *Runner {
	cfg := *r
	if cfg.PwshPath == "" {
		cfg.PwshPath = "pwsh"
	}
	if cfg.MaxStdin <= 0 {
		cfg.MaxStdin = DefaultMaxStdin
	}
	if cfg.MaxStdout <= 0 {
		cfg.MaxStdout = DefaultMaxStdout
	}
	if cfg.MaxStderr <= 0 {
		cfg.MaxStderr = DefaultMaxStderr
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &cfg
}

func (r *Runner) writeTrustedScript(script TrustedScript) (string, func(), error) {
	name := sanitizeScriptName(script.Name)
	if name == "" {
		name = "bridge.ps1"
	}

	tempDir, err := os.MkdirTemp(r.TempDir, "intraspect-")
	if err != nil {
		return "", nil, fmt.Errorf("create PowerShell temp directory: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	scriptPath := filepath.Join(tempDir, name)
	content := encodingPrelude + script.Content
	if err := os.WriteFile(scriptPath, []byte(content), 0600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write trusted PowerShell script: %w", err)
	}

	return scriptPath, cleanup, nil
}

func sanitizeScriptName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	if !strings.HasSuffix(strings.ToLower(name), ".ps1") {
		name += ".ps1"
	}
	return name
}

type boundedBuffer struct {
	buf        bytes.Buffer
	limit      int64
	overflowed bool
}

func newBoundedBuffer(limit int64) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.overflowed = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.overflowed = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *boundedBuffer) String() string {
	out, err := io.ReadAll(bytes.NewReader(b.buf.Bytes()))
	if err != nil {
		return ""
	}
	return string(out)
}
