// mockai is a developer-only mock of an OpenAI-compatible chat
// endpoint that streams a canned response as an SSE stream — the shape
// the built-in pi agent (and pi's openai-completions API) expects.
//
// It exists so a real, unsanitized end-to-end run of the built-in
// agent can be exercised without a live model, without spending
// provider credits, and without leaking secrets into a provider:
//
//	go run ./hack/mockai --port 8765 --response-file /tmp/canned.txt &
//
// Then point shipyard at it, with a dummy model name:
//
//	shipyard solve --repo <repo> --issue <n> \
//	  --provider custom --ai-endpoint http://127.0.0.1:8765/v1 \
//	  --ai-model mock-model ...
//
// The agent sees the response as one assistant turn (chunked content
// deltas ending with a stop finish). The response file should contain text an agent can act
// on — instructions and code it writes or edits in the checkout — not
// a raw diff (the agent applies changes itself).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

func main() {
	port := flag.Int("port", 8765, "TCP port to listen on")
	responseFile := flag.String("response-file", "", "file whose contents are returned as the model response")
	defaultText := flag.String("text", "Done. (mockai: no --response-file given)", "response text when --response-file is unset")
	flag.Parse()

	response := *defaultText
	if *responseFile != "" {
		data, err := os.ReadFile(*responseFile)
		if err != nil {
			log.Fatalf("reading --response-file: %v", err)
		}
		response = string(data)
	}

	log.Printf("mockai: listening on :%d (response: %d bytes)", *port, len(response))
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), newHandler(response)); err != nil {
		log.Fatal(err)
	}
}

// newHandler wires the mock endpoint: a health check on GET / and the
// streaming chat completion on POST /v1/chat/completions.
func newHandler(response string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "mockai ok")
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		// One assistant turn: chunked content deltas, then a stop
		// finish (the agent ends its turn; as a mock there is no tool
		// call to continue the loop with).
		writeSSE(w, fl, chunk([]map[string]any{
			{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil},
		}))
		for _, piece := range chunkText(response, 64) {
			writeSSE(w, fl, chunk([]map[string]any{
				{"index": 0, "delta": map[string]any{"content": piece}, "finish_reason": nil},
			}))
		}
		writeSSE(w, fl, chunk([]map[string]any{
			{"index": 0, "delta": map[string]any{"content": ""}, "finish_reason": "stop"},
		}))
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	})
	return mux
}

// chunk builds one SSE payload: a chat.completion.chunk wrapper.
func chunk(choices []map[string]any) any {
	return map[string]any{
		"id":      "cmpl-mockai",
		"object":  "chat.completion.chunk",
		"choices": choices,
	}
}

func writeSSE(w io.Writer, f http.Flusher, payload any) {
	marshaled, err := json.Marshal(payload)
	if err != nil {
		log.Printf("mockai: %v", err)
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", marshaled)
	f.Flush()
}

func chunkText(s string, size int) []string {
	if size <= 0 {
		size = 64
	}
	var out []string
	for len(s) > 0 {
		n := size
		if n > len(s) {
			n = len(s)
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}
