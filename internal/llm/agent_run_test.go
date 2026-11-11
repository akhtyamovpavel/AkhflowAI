package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunReportsStepLimitAndRecordsHooks(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"unknown","arguments":"{}"}}]}}]}`)
	}))
	defer model.Close()
	before := 0
	after := 0
	agent := Agent{Client: NewClient(model.URL, "fixture")}
	agent.BeforeTool = func(iteration, sequence int, name string) error { before++; return nil }
	agent.AfterTool = func(iteration, sequence int, name string, ok bool) error { after++; return nil }
	result, err := agent.Run(context.Background(), "hello", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.StepLimit || result.Iterations != 8 || before != 8 || after != 8 {
		t.Fatalf("result=%+v before=%d after=%d", result, before, after)
	}
}
func TestRunStopsBeforeToolWhenStepCannotBeRecorded(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"unknown","arguments":"{}"}}]}}]}`)
	}))
	defer model.Close()
	after := false
	agent := Agent{Client: NewClient(model.URL, "fixture")}
	agent.BeforeTool = func(iteration, sequence int, name string) error { return fmt.Errorf("recording failed") }
	agent.AfterTool = func(iteration, sequence int, name string, ok bool) error { after = true; return nil }
	_, err := agent.Run(context.Background(), "hello", "", nil)
	if err == nil || after {
		t.Fatal("agent continued after recording failed")
	}
}
