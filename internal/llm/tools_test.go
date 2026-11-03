package llm

import (
	"encoding/json"
	"testing"
)

func TestInputSchemaKeepsNullableProperties(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{
		"query":{"type":"string","description":"The search query"},
		"productFilter":{"type":["string","null"],"default":null}},
		"required":["query"],"additionalProperties":false}`), &m); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(inputSchema(m))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(b, &got)
	props, _ := got["properties"].(map[string]any)
	if got["type"] != "object" || props["query"] == nil || props["productFilter"] == nil ||
		got["additionalProperties"] != false || len(got["required"].([]any)) != 1 {
		t.Errorf("schema = %s", b)
	}
}
