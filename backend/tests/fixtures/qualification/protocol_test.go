package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBrowserFixtureAcceptsExplicitTrialEnvelope(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": "候选任务\n<capability-trial-data>\n{\"input\":{\"source\":\"完整资料🌙\"}}\n</capability-trial-data>"}}})
	request, err := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	response, err := (protocolTransport{}).RoundTrip(request)
	if err != nil {
		t.Fatal("trial envelope rejected by tests-only transport", err)
	}
	defer response.Body.Close()
	var body struct {
		Choices []struct{ Message struct{ Content string } }
	}
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil || len(body.Choices) != 1 || !strings.Contains(body.Choices[0].Message.Content, "完整资料🌙") {
		t.Fatal(body, err)
	}
}
