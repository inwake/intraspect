package shell

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunnerRoundTripsJSONThroughStdin(t *testing.T) {
	runner := NewRunner()
	script := TrustedScript{
		Name: "roundtrip.ps1",
		Content: `
$ErrorActionPreference = 'Stop'
$Request = [Console]::In.ReadToEnd() | ConvertFrom-Json -ErrorAction Stop
[pscustomobject]@{
  value = $Request.value
} | ConvertTo-Json -Compress
`,
	}
	request := []byte(`{"value":"quote: \" newline:\n unicode: кафе dollar: $HOME"}`)

	out, err := runner.Run(context.Background(), script, request)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var got struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out)
	}
	want := "quote: \" newline:\n unicode: кафе dollar: $HOME"
	if got.Value != want {
		t.Fatalf("value mismatch\nwant: %q\n got: %q", want, got.Value)
	}
}

func TestRunnerRejectsOversizedInput(t *testing.T) {
	runner := NewRunner()
	runner.MaxStdin = 8
	_, err := runner.Run(
		context.Background(),
		TrustedScript{Name: "noop.ps1", Content: `[Console]::Out.Write('{}')`},
		[]byte(`{"too":"large"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "stdin limit") {
		t.Fatalf("expected stdin limit error, got %v", err)
	}
}

func TestRunnerRejectsOversizedStdout(t *testing.T) {
	runner := NewRunner()
	runner.MaxStdout = 8
	_, err := runner.Run(
		context.Background(),
		TrustedScript{Name: "large.ps1", Content: `[Console]::Out.Write('0123456789')`},
		[]byte(`{}`),
	)
	if err == nil || !strings.Contains(err.Error(), "stdout exceeded") {
		t.Fatalf("expected stdout limit error, got %v", err)
	}
}

func TestRunnerRejectsOversizedStderr(t *testing.T) {
	runner := NewRunner()
	runner.MaxStderr = 8
	_, err := runner.Run(
		context.Background(),
		TrustedScript{Name: "stderr.ps1", Content: `[Console]::Error.Write('0123456789'); exit 1`},
		[]byte(`{}`),
	)
	if err == nil || !strings.Contains(err.Error(), "stderr exceeded") {
		t.Fatalf("expected stderr limit error, got %v", err)
	}
}

func TestRunnerEnforcesTimeout(t *testing.T) {
	runner := NewRunner()
	runner.Timeout = 100 * time.Millisecond
	_, err := runner.Run(
		context.Background(),
		TrustedScript{Name: "slow.ps1", Content: `Start-Sleep -Seconds 5; [Console]::Out.Write('{}')`},
		[]byte(`{}`),
	)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestRunnerCleansUpTemporaryScriptDirectory(t *testing.T) {
	parent := t.TempDir()
	runner := NewRunner()
	runner.TempDir = parent
	_, err := runner.Run(
		context.Background(),
		TrustedScript{Name: "cleanup.ps1", Content: `[Console]::Out.Write('{}')`},
		[]byte(`{}`),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read temp parent: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "intraspect-") {
			t.Fatalf("temporary script directory was not cleaned up: %s", filepath.Join(parent, entry.Name()))
		}
	}
}

func TestHideWindowSetsWindowsSysProcAttr(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only assertion")
	}
	cmd := exec.Command("pwsh", "-NoProfile", "-Command", "$PSVersionTable.PSVersion")
	hideWindow(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatalf("expected SysProcAttr to be set")
	}
	hideWindowField := reflect.ValueOf(cmd.SysProcAttr).Elem().FieldByName("HideWindow")
	if !hideWindowField.Bool() {
		t.Fatalf("expected HideWindow to be set")
	}
}
