package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCheckpointRejectsPathTraversalSessionID(t *testing.T) {
	for _, id := range []string{"../outside", "session_../../outside", "session_/tmp/evil", "not-a-session"} {
		if _, err := NewCheckpointStore(id); err == nil || !strings.Contains(err.Error(), "非法 session_id") {
			t.Fatalf("NewCheckpointStore(%q) err=%v", id, err)
		}
		if _, err := LoadCheckpoint(id); err == nil || !strings.Contains(err.Error(), "非法 session_id") {
			t.Fatalf("LoadCheckpoint(%q) err=%v", id, err)
		}
	}
}

func TestCheckpointRedactsConfiguredSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := config.GlobalConfig
	config.GlobalConfig.ApiKey = "api-secret-value-123"
	config.GlobalConfig.Targets = []config.TargetConfig{{Password: "ssh-secret-value-456"}}
	defer func() { config.GlobalConfig = old }()

	store, err := NewCheckpointStore("session_redaction")
	if err != nil {
		t.Fatal(err)
	}
	history := []analyzer.Message{{Role: "user", Content: "api_key: api-secret-value-123 password=ssh-secret-value-456"}}
	if err := store.Save(CheckpointData{State: NewAgentState(""), History: history}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.SessionDir() + "/checkpoint.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"api-secret-value-123", "ssh-secret-value-456"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("checkpoint leaked %q:\n%s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "***") {
		t.Fatalf("checkpoint should retain redaction marker: %s", raw)
	}
}

func TestSessionSummariesSortNewestFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	olderID := "session_100"
	newerID := "session_200"
	for _, tc := range []struct {
		id    string
		stamp time.Time
	}{{olderID, time.Now().Add(-time.Hour)}, {newerID, time.Now()}} {
		store, err := NewCheckpointStore(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(CheckpointData{State: NewAgentState(""), UserGoal: tc.id}); err != nil {
			t.Fatal(err)
		}
		path := store.SessionDir() + "/checkpoint.json"
		data, err := LoadCheckpoint(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		data.SavedAt = tc.stamp
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	summaries, err := ListSessionSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 || summaries[0].ID != newerID {
		t.Fatalf("summaries not newest first: %#v", summaries)
	}
}

func TestCheckpointUserGoalIgnoresToolFeedback(t *testing.T) {
	got := checkpointUserGoal([]analyzer.Message{
		{Role: "user", Content: "Output:\nsynthetic"},
		{Role: "user", Content: "需求：排查 SSH 暴力破解"},
		{Role: "user", Content: "后续追问"},
	})
	if got != "排查 SSH 暴力破解" {
		t.Fatalf("goal=%q", got)
	}
}

func TestCheckpointAcceptsGeneratedSessionID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := NewSessionID()
	store, err := NewCheckpointStore(id)
	if err != nil {
		t.Fatal(err)
	}
	if store.sessionID != id {
		t.Fatalf("session=%q want %q", store.sessionID, id)
	}
	for step := 1; step <= 2; step++ {
		if err := store.Save(CheckpointData{StepNum: step, State: NewAgentState("")}); err != nil {
			t.Fatalf("save step %d: %v", step, err)
		}
	}
	loaded, err := LoadCheckpoint(id)
	if err != nil || loaded.StepNum != 2 {
		t.Fatalf("replaced checkpoint step=%v err=%v", loaded, err)
	}
}

func TestCheckpointPersistsCoreClueBoard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "session_core_clues"
	store, err := NewCheckpointStore(id)
	if err != nil {
		t.Fatal(err)
	}
	state := NewAgentState("")
	state.ObserveCoreClues("关键结论：攻击源 198.51.100.7，文件 /var/www/html/x.php", "subagent/log")
	if err := store.Save(CheckpointData{State: state}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint(id)
	if err != nil {
		t.Fatal(err)
	}
	prompt := loaded.State.CoreCluesPrompt(4000)
	if !strings.Contains(prompt, "198.51.100.7") || !strings.Contains(prompt, "/var/www/html/x.php") {
		t.Fatalf("checkpoint lost core clues:\n%s", prompt)
	}
}
