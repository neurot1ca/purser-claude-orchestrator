package contextio

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoardDisplaySkipsHeldApplyLock(t *testing.T) {
	parent := filepath.Join("..", "..", "..", ".tmp")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parent, "board-lock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root = resolve(root)
	t.Setenv("PURSER_STATE_DIR", root)
	id := strings.Repeat("a", 64)
	path := filepath.Join(root, "codex_exec_mcp", "jobs", id+".json")
	if err := writeJSONAtomically(path, map[string]any{
		"jobId": id, "state": "SUCCEEDED", "repoRoot": root, "boardDisplayCount": 0,
	}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(root))
	release, err := LockFile(filepath.Join(root, "codex_exec_mcp", "apply", hex.EncodeToString(digest[:])+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := map[string]any{"board_job_ids": []any{id}}
	markBoardDisplayed(dispatch, "turn")
	job, ok := readSmallObject(path)
	if !ok || number(job["boardDisplayCount"]) != 0 {
		t.Fatal("display updated despite held apply lock")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	markBoardDisplayed(dispatch, "turn")
	job, ok = readSmallObject(path)
	if !ok || number(job["boardDisplayCount"]) != 1 {
		t.Fatal("skipped display was marked as counted")
	}
}

func TestDispatchAdviceBoardLanguage(t *testing.T) {
	for _, language := range []string{"Turkish", "English", ""} {
		t.Run(language, func(t *testing.T) {
			parent := filepath.Join("..", "..", "..", ".tmp")
			if err := os.MkdirAll(parent, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(parent, "board-language-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			t.Setenv("PURSER_STATE_DIR", resolve(root))
			if language != "" {
				RecordUserLanguage(root, language)
			}
			want := "Include this latest job board verbatim:\nEnglish board"
			if language == "Turkish" {
				want = "Render this job board in Turkish, translating from English while keeping ⚓, markdown table structure, row order, model names, task labels, ETAs and dashes unchanged.\nEnglish board"
			}
			for _, lines := range [][]any{{}, {"Dispatch receipt"}} {
				got := dispatchAdvice(map[string]any{"lines": lines, "board": "English board"}, "fixture")
				if !strings.HasSuffix(got, want) || (len(lines) > 0 && !strings.Contains(got, "Dispatch receipt\n")) {
					t.Fatalf("dispatch advice = %q, want board instruction %q", got, want)
				}
			}
		})
	}
}

func TestBoardDisplayedTranslated(t *testing.T) {
	const header = "> | ⚓ Task | Model | State | ETA | Review | Apply |\n> | --- | --- | --- | --- | --- | --- |\n"
	const first = "> | Fix \\| parser | gpt-6-sol (medium) | coding | 2m | — | — |"
	const second = "> | Audit | Claude | done | — | reviewed | applied |"
	board := "Purser jobs:\n" + header + first + "\n" + second
	translated := strings.NewReplacer(
		"Purser jobs:", "Purser işleri:", "⚓ Task | Model | State | ETA | Review | Apply", "⚓ Görev | Model | Durum | Süre | İnceleme | Uygulama",
		"coding", "kodlanıyor", "done", "bitti", "reviewed", "incelendi", "applied", "uygulandı",
	).Replace(board)
	for _, tc := range []struct {
		name, reply string
		want        bool
	}{
		{"English", board, true},
		{"translated", "Önceki metin\r\n" + strings.ReplaceAll(translated, "\n", "\r\n") + "\r\nSonraki metin", true},
		{"no anchor", strings.ReplaceAll(translated, "⚓", ""), false},
		{"missing row", "Purser jobs:\n" + header + first, false},
		{"extra row", translated + "\n" + second, false},
		{"reordered rows", "Purser jobs:\n" + header + second + "\n" + first, false},
		{"changed task", strings.ReplaceAll(translated, "Audit", "Other"), false},
		{"changed model", strings.ReplaceAll(translated, "gpt-6-sol", "Other"), false},
		{"changed ETA", strings.ReplaceAll(translated, "2m", "3m"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := boardDisplayed(tc.reply, board); got != tc.want {
				t.Fatalf("boardDisplayed() = %v, want %v", got, tc.want)
			}
		})
	}
}
