package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yonderllm/internal/perm"
)

func TestWriteRechecksCancellationAndApprovalDiff(t *testing.T) {
	for _, mode := range []string{"cancel", "changed"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			path := filepath.Join(dir, "file.txt")
			if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tool := writeTool(perm.New(perm.Code), func(context.Context, Request) bool {
				if mode == "cancel" {
					cancel()
				} else if err := os.WriteFile(path, []byte("user edit"), 0o600); err != nil {
					t.Fatal(err)
				}
				return true
			})
			_, err := tool.Run(ctx, `{"path":"file.txt","content":"model edit"}`)
			if err == nil {
				t.Fatal("stale/cancelled approval wrote file")
			}
			if mode == "changed" && !strings.Contains(err.Error(), "changed") {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "original"
			if mode == "changed" {
				want = "user edit"
			}
			if string(data) != want {
				t.Fatalf("overwrote unapproved content: %q", data)
			}
		})
	}
}

func TestWriteDetectsEditsHiddenByDiffSummary(t *testing.T) {
	t.Chdir(t.TempDir())
	old := strings.Repeat("old\n", 1100)
	edited := strings.Repeat("edited\n", 1100)
	if err := os.WriteFile("file.txt", []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := writeTool(perm.New(perm.Code), func(context.Context, Request) bool {
		if err := os.WriteFile("file.txt", []byte(edited), 0o600); err != nil {
			t.Fatal(err)
		}
		return true
	})
	args, err := json.Marshal(map[string]string{"path": "file.txt", "content": strings.Repeat("new\n", 1100)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), string(args)); err == nil {
		t.Error("summary collision authorized stale write")
	}
	data, err := os.ReadFile("file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != edited {
		t.Error("concurrent edit overwritten")
	}
}
