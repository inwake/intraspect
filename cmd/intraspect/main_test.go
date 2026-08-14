package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunReportsFatalErrorsToStderr(t *testing.T) {
	var stderr bytes.Buffer
	code := run(func() error {
		return errors.New("boom")
	}, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "intraspect: boom") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
