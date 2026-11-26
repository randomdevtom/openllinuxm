package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Server is one local OpenAI-compatible endpoint (Quarry, Ollama, llama.cpp, ...).
type Server struct {
	Name   string   // display name
	URL    string   // base URL without /v1
	Kind   string   // "quarry", "ollama" or "openai"
	Models []string // model ids as the server names them
	Label  string   // Quarry: the GGUF file it serves
}

// well-known local ports; anything else can be given with --url
var candidates = []struct{ name, url string }{
	{"Quarry", "http://127.0.0.1:8090"},
	{"Ollama", "http://127.0.0.1:11434"},
	{"llama.cpp", "http://127.0.0.1:8080"},
	{"LM Studio", "http://127.0.0.1:1234"},
	{"vLLM", "http://127.0.0.1:8000"},
	{"Jan", "http://127.0.0.1:1337"},
	{"KoboldCpp", "http://127.0.0.1:5001"},
}

var probeClient = &http.Client{Timeout: 1500 * time.Millisecond}

func getJSON(ctx context.Context, c *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// Probe asks one base URL what it is and which models it has.
func Probe(ctx context.Context, name, base string) (*Server, error) {
	s := &Server{Name: name, URL: strings.TrimRight(base, "/"), Kind: "openai"}
	var st QuarryStats
	if getJSON(ctx, probeClient, s.URL+"/v1/stats", &st) == nil && st.Arch != "" {
		s.Kind, s.Name, s.Label = "quarry", "Quarry", st.Model
	}
	var tags struct {
		Models []struct{ Name string } `json:"models"`
	}
	if getJSON(ctx, probeClient, s.URL+"/api/tags", &tags) == nil && len(tags.Models) > 0 {
		if s.Kind != "quarry" {
			s.Kind, s.Name = "ollama", "Ollama"
		}
		for _, m := range tags.Models {
			s.Models = append(s.Models, m.Name)
		}
	}
	if len(s.Models) == 0 {
		var ml struct {
			Data []struct{ ID string } `json:"data"`
		}
		if err := getJSON(ctx, probeClient, s.URL+"/v1/models", &ml); err != nil {
			return nil, err
		}
		for _, m := range ml.Data {
			s.Models = append(s.Models, m.ID)
		}
	}
	if len(s.Models) == 0 {
		return nil, errors.New("no models")
	}
	sort.Strings(s.Models)
	return s, nil
}

// Discover probes the well-known ports (and any extra URLs) in parallel.
func Discover(ctx context.Context, extra []string) []*Server {
	type job struct{ name, url string }
	var jobs []job
	for _, u := range extra {
		jobs = append(jobs, job{"Custom", u})
	}
	for _, c := range candidates {
		jobs = append(jobs, job{c.name, c.url})
	}
	out := make([]*Server, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			if s, err := Probe(ctx, j.name, j.url); err == nil {
				out[i] = s
			}
		}(i, j)
	}
	wg.Wait()
	var res []*Server
	seen := map[string]bool{}
	for _, s := range out {
		if s != nil && !seen[s.URL] {
			seen[s.URL] = true
			res = append(res, s)
		}
	}
	return res
}

// ---------------- Quarry extras ----------------

type QuarryStats struct {
	Model    string  `json:"model"`
	Arch     string  `json:"arch"`
	NCtx     int     `json:"n_ctx"`
	CtxUsed  int     `json:"ctx_used"`
	CacheHit float64 `json:"cache_hit"`
	Last     struct {
		PromptTokens int     `json:"prompt_tokens"`
		PromptS      float64 `json:"prompt_s"`
		GenTokens    int     `json:"gen_tokens"`
		GenS         float64 `json:"gen_s"`
	} `json:"last"`
}

func (s *Server) Stats(ctx context.Context) (*QuarryStats, error) {
	var st QuarryStats
	if err := getJSON(ctx, probeClient, s.URL+"/v1/stats", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// CancelRemote tells Quarry to stop the running generation (closing the stream also works,
// but Quarry may still be reading the prompt and only notices at the next token).
func (s *Server) CancelRemote() {
	if s.Kind != "quarry" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/v1/cancel", nil)
	if resp, err := probeClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

// ---------------- chat ----------------

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// the model's thinking for this reply, sent back so the server can render the history as
	// generated and keep its prompt cache (Quarry, llama.cpp, vLLM read this field)
	Reasoning  string     `json:"reasoning_content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// StreamEvent is one piece of a streamed reply.
type StreamEvent struct {
	Text, Reasoning string
}

type ChatResult struct {
	Content, Reasoning string
	ToolCalls          []ToolCall
	Finish             string
	Chunks             int
}

// ErrToolsUnsupported: the server refused the request because of the tools field.
var ErrToolsUnsupported = errors.New("server does not support tool calling")

var chatClient = &http.Client{} // no timeout: local models can take many minutes on a long prompt

// Chat streams one completion. on is called for every text or reasoning piece.
func (s *Server) Chat(ctx context.Context, model string, msgs []Message, tools []any, think bool, on func(StreamEvent)) (*ChatResult, error) {
	body := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   true,
		// servers default to short replies (Quarry: 1024 tokens), which cuts big file writes in half
		"max_tokens": 16384,
		// thinking on/off: Quarry and llama.cpp read the template kwarg, Quarry also the top-level key
		"chat_template_kwargs": map[string]any{"enable_thinking": think},
		"enable_thinking":      think,
	}
	if s.Kind == "ollama" {
		body["think"] = think
		delete(body, "enable_thinking")
		delete(body, "chat_template_kwargs")
	}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", s.URL+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer local")
	resp, err := chatClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(b))
		if len(tools) > 0 && resp.StatusCode == 400 && strings.Contains(strings.ToLower(msg), "tool") {
			return nil, ErrToolsUnsupported
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	res := &ChatResult{}
	var content, reasoning strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
					ToolCalls        []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			return nil, errors.New(chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		r := d.ReasoningContent + d.Reasoning
		if r != "" {
			reasoning.WriteString(r)
			res.Chunks++
			on(StreamEvent{Reasoning: r})
		}
		if d.Content != "" {
			content.WriteString(d.Content)
			res.Chunks++
			on(StreamEvent{Text: d.Content})
		}
		for _, tc := range d.ToolCalls {
			i := len(res.ToolCalls)
			if tc.Index != nil {
				i = *tc.Index
			}
			for len(res.ToolCalls) <= i {
				res.ToolCalls = append(res.ToolCalls, ToolCall{Type: "function"})
			}
			c := &res.ToolCalls[i]
			if tc.ID != "" {
				c.ID = tc.ID
			}
			c.Function.Name += tc.Function.Name
			if a := tc.Function.Arguments; len(a) > 0 {
				var str string
				if json.Unmarshal(a, &str) == nil {
					c.Function.Arguments += str
				} else {
					c.Function.Arguments += string(a) // some servers send the object itself
				}
			}
		}
		if fr := chunk.Choices[0].FinishReason; fr != nil {
			res.Finish = *fr
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return nil, err
	}
	res.Content, res.Reasoning = content.String(), reasoning.String()
	for i := range res.ToolCalls {
		if res.ToolCalls[i].ID == "" {
			res.ToolCalls[i].ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
		}
	}
	return res, ctx.Err()
}
