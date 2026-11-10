package toolbox

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeToolArguments(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{"task_id": map[string]any{}, "status": map[string]any{}},
	}
	invalid := []string{
		`null`,
		`[]`,
		`{"task_id":1} {}`,
		`{"task_id":1,"task_id":2}`,
		`{"task_id":1,"TASK_ID":2}`,
		`{"task_id":1,"\u0054ASK_ID":2}`,
		`{"task_id":1,"taſk_id":2}`,
		`{"status":"todo","STATUS":"done","task_id":1}`,
	}
	for _, input := range invalid {
		if _, err := normalizeToolArguments(json.RawMessage(input), schema, false); err == nil {
			t.Errorf("accepted ambiguous or invalid JSON: %s", input)
		}
	}
	result, err := normalizeToolArguments(json.RawMessage(`{"TASK_ID":123,"STATUS":"todo","other":{"nested":true}}`), schema, false)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["task_id"]) != "123" || string(decoded["status"]) != `"todo"` || string(decoded["other"]) != `{"nested":true}` {
		t.Errorf("unexpected normalized JSON: %s", result)
	}
	if _, err := normalizeToolArguments(json.RawMessage(`{}`), schema, true); err == nil {
		t.Error("accepted a schema without its project resource")
	}
	fields := make([]string, 129)
	for i := range fields {
		fields[i] = fmt.Sprintf(`"extra%d":0`, i)
	}
	large := "{" + strings.Join(fields, ",") + "}"
	if _, err := normalizeToolArguments(json.RawMessage(large), schema, false); err == nil {
		t.Error("accepted an unbounded number of tool arguments")
	}
}
