package agentcli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskDeliveryRuntimeMessageContainsOnlyTaskResultFields(t *testing.T) {
	message := taskDelivery{Result: TaskResult{
		TaskID: "task_1", AgentName: "researcher", State: TaskStateCompleted, Output: "done",
	}, Metadata: map[string]any{"requires_requester_reply": true}}.RuntimeMessage()
	if message.Type != "runtime_event" || !strings.HasPrefix(message.Content, "<task_result>\n") || !strings.HasSuffix(message.Content, "\n</task_result>") {
		t.Fatalf("task runtime message = %#v", message)
	}
	contents := strings.TrimSuffix(strings.TrimPrefix(message.Content, "<task_result>\n"), "\n</task_result>")
	var payload map[string]any
	if err := json.Unmarshal([]byte(contents), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"task_id", "agent", "state", "output"} {
		if _, found := payload[key]; !found {
			t.Fatalf("task payload = %#v, missing %q", payload, key)
		}
	}
	for _, forbidden := range []string{"metadata", "result_progress", "instruction", "subagent_id"} {
		if _, found := payload[forbidden]; found {
			t.Fatalf("task payload leaked %q: %#v", forbidden, payload)
		}
	}
}
