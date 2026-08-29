// Command mockai is a tiny OpenAI-compatible chat completions server for
// testing Shipyard locally without a real AI endpoint:
//
//	mockai --port 8765 --response-file canned-response.md
//	mockai --port 8765 --response 'explanation here
//	```diff
//	--- a/f.go
//	+++ b/f.go
//	@@ -1 +1 @@
//	-old
//	+new
//	```'
//
// Any POST (typically /chat/completions) receives
// {"model": ..., "messages": [...]} and gets back the configured
// response wrapped in the standard chat-completions envelope, so
// SHIPYARD_AI_ENDPOINT can point at http://127.0.0.1:8765/v1.
//
// The requests it receives (model and prompt) are logged to stderr.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := flag.Int("port", 8765, "TCP port to listen on")
	respFile := flag.String("response-file", "", "file containing the canned AI response")
	resp := flag.String("response", "", "canned AI response (inline)")
	flag.Parse()

	body := ""
	if *respFile != "" {
		data, err := os.ReadFile(*respFile)
		if err != nil {
			log.Fatalf("mockai: reading --response-file: %v", err)
		}
		body = string(data)
	} else if *resp != "" {
		body = *resp
	} else {
		log.Fatal("mockai: pass --response or --response-file")
	}

	serve := func(w http.ResponseWriter, r *http.Request) {
		// Log the prompt so you can inspect what Shipyard sent.
		fmt.Fprintf(os.Stderr, "mockai: %s %s\n", r.Method, r.URL.Path)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Messages) > 0 {
			fmt.Fprintf(os.Stderr, "mockai: model=%s prompt=%q\n", req.Model, req.Messages[0].Content)
		}
		w.Header().Set("Content-Type", "application/json")
		envelope := map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": body}}},
		}
		if err := json.NewEncoder(w).Encode(envelope); err != nil {
			log.Printf("mockai: writing response: %v", err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", serve) // accept any path: /v1/chat/completions, /chat/completions, ...
	log.Printf("mockai listening on 127.0.0.1:%d (use SHIPYARD_AI_ENDPOINT=http://127.0.0.1:%d/v1)", *port, *port)
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), mux); err != nil {
		log.Fatal(err)
	}
}
