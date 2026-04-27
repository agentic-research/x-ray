package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "report.json")

	report := IntegrationBenchReport{
		Timestamp: "2026-04-27T00:00:00Z",
		ConfigA:   "test-a",
		ConfigB:   "test-b",
		AccuracyA: 0.8,
		AccuracyB: 0.9,
	}

	if err := writeReport(path, report); err != nil {
		t.Fatalf("writeReport: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if len(data) == 0 {
		t.Error("report file is empty")
	}
}
