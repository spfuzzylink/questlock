package inference

import "encoding/json"

// BoundedTextPrompt serializes the explicit user request for text-only native
// execution and matched baselines. Harness/system context can still differ.
func BoundedTextPrompt(req Request) string {
	b, _ := json.Marshal(struct {
		Instructions string `json:"instructions"`
		Input        string `json:"input"`
	}{req.Prefix, req.Input})
	return "Answer the explicitly approved request below using only its supplied context. Produce the requested final answer directly. Do not use tools, inspect files, access networks, delegate, or change settings. The JSON separates the request instructions from its input data.\n" + string(b)
}
