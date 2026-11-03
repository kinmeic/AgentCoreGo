package main

import (
	"agent-core/internal/config"
	"agent-core/internal/mcp"
	"agent-core/internal/model"
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const agentCoreVersion = "0.3.1"

type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newAPIClient(baseURL, token string) *apiClient {
	return &apiClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{}}
}

func clientFlags(set *flag.FlagSet) (*string, *string) {
	baseURL := os.Getenv("AGENT_CORE_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8900"
	}
	return set.String("url", baseURL, "AgentCore base URL"), set.String("token", os.Getenv("AUTH_TOKEN"), "API bearer token")
}

func (c *apiClient) request(method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(request)
}

func (c *apiClient) do(method, path string, body any) ([]byte, error) {
	response, err := c.request(method, path, body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func runCLI(args []string) (bool, int) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || args[0] == "mcp-serve" {
		return false, 0
	}
	var err error
	switch args[0] {
	case "version":
		fmt.Println(agentCoreVersion)
		return true, 0
	case "chat":
		err = cliChat(args[1:])
	case "resume":
		err = cliResume(args[1:])
	case "stop":
		err = cliStop(args[1:])
	case "conversations":
		err = cliConversations(args[1:])
	case "logs":
		err = cliLogs(args[1:])
	case "tools":
		err = cliTools(args[1:])
	case "skills":
		err = cliSkills(args[1:])
	case "workflows":
		err = cliWorkflows(args[1:])
	case "snapshots":
		err = cliSnapshots(args[1:])
	case "trust":
		err = cliTrust(args[1:])
	case "benchmarks":
		err = cliBenchmarks(args[1:])
	case "memory":
		err = cliMemory(args[1:])
	case "mcp":
		err = cliMCP(args[1:])
	case "doctor":
		err = cliDoctor(args[1:])
	case "config":
		err = cliConfig(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		return true, 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return true, 1
	}
	return true, 0
}

func cliChat(args []string) error {
	set := flag.NewFlagSet("chat", flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	conversation := set.String("conversation", "", "conversation ID")
	message := set.String("message", "", "message text")
	stream := set.Bool("stream", true, "stream SSE events")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *message == "" {
		*message = strings.Join(set.Args(), " ")
	}
	if strings.TrimSpace(*message) == "" {
		return fmt.Errorf("message is required")
	}
	client := newAPIClient(*baseURL, *token)
	request := model.ChatRequest{ConversationID: *conversation, Message: *message, Source: "cli"}
	if *stream {
		return cliStreamChat(client, "/v1/chat/stream", request)
	}
	data, err := client.do(http.MethodPost, "/v1/chat", request)
	if err != nil {
		return err
	}
	var response model.ChatResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	fmt.Println(response.Reply)
	fmt.Fprintf(os.Stderr, "conversation=%s run=%s status=%s\n", response.ConversationID, response.RunID, response.Status)
	return nil
}

func cliStreamChat(client *apiClient, path string, body any) error {
	response, err := client.request(http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, data)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	input := bufio.NewReader(os.Stdin)
	printed := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event model.StreamEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			return fmt.Errorf("decode SSE event: %w", err)
		}
		switch event.Type {
		case "chunk":
			fmt.Print(event.Content)
			printed = true
		case "tool.started":
			fmt.Fprintf(os.Stderr, "\n[tool] %s\n", event.ToolName)
		case "file.changed":
			fmt.Fprintf(os.Stderr, "[file] %s\n", event.Content)
		case "approval.required":
			answer := promptLine(input, fmt.Sprintf("Allow %s? [y]es/[a]ll conversation/[n]o: ", event.ToolName))
			decision, scope := "deny", "once"
			switch strings.ToLower(answer) {
			case "y", "yes":
				decision = "allow"
			case "a", "all":
				decision, scope = "allow", "conversation"
			}
			if _, err := client.do(http.MethodPost, "/v1/approvals/"+event.RequestID,
				map[string]string{"decision": decision, "scope": scope}); err != nil {
				return err
			}
		case "clarify":
			fmt.Fprintf(os.Stderr, "\nconversation=%s run=%s\n", event.ConversationID, event.RunID)
			answer := promptLine(input, event.ClarifyMessage+" ")
			if _, err := client.do(http.MethodPost, "/v1/chat/"+event.ConversationID+"/clarify",
				map[string]string{"response": answer}); err != nil {
				return err
			}
		case "done":
			if !printed {
				fmt.Print(event.Reply)
			}
			fmt.Println()
			fmt.Fprintf(os.Stderr, "conversation=%s run=%s status=completed\n", event.ConversationID, event.RunID)
			return nil
		case "cancelled":
			return fmt.Errorf("run cancelled")
		case "error":
			return fmt.Errorf("%s", event.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func promptLine(reader *bufio.Reader, prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	answer, _ := reader.ReadString('\n')
	return strings.TrimSpace(answer)
}

func cliResume(args []string) error {
	set := flag.NewFlagSet("resume", flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	run := set.String("run", "", "run ID")
	strategy := set.String("strategy", "", "interrupted tool strategy: retry or skip")
	stream := set.Bool("stream", true, "stream SSE")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *run == "" {
		return fmt.Errorf("--run is required")
	}
	client := newAPIClient(*baseURL, *token)
	body := map[string]any{"strategy": *strategy, "stream": *stream}
	if *stream {
		return cliStreamChat(client, "/v1/runs/"+*run+"/resume", body)
	}
	data, err := client.do(http.MethodPost, "/v1/runs/"+*run+"/resume", body)
	if err != nil {
		return err
	}
	var response model.ChatResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	fmt.Println(response.Reply)
	return nil
}

func cliStop(args []string) error {
	set := flag.NewFlagSet("stop", flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	run := set.String("run", "", "run ID")
	conversation := set.String("conversation", "", "conversation ID")
	if err := set.Parse(args); err != nil {
		return err
	}
	path := ""
	if *run != "" {
		path = "/v1/runs/" + *run + "/stop"
	} else if *conversation != "" {
		path = "/v1/chat/" + *conversation + "/stop"
	} else {
		return fmt.Errorf("--run or --conversation is required")
	}
	return printJSON(newAPIClient(*baseURL, *token).do(http.MethodPost, path, map[string]any{}))
}

func cliConversations(args []string) error {
	set := flag.NewFlagSet("conversations", flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	limit := set.Int("limit", 50, "maximum conversations")
	if err := set.Parse(args); err != nil {
		return err
	}
	return printJSON(newAPIClient(*baseURL, *token).do(http.MethodGet, "/v1/conversations?limit="+strconv.Itoa(*limit), nil))
}

func cliLogs(args []string) error {
	set := flag.NewFlagSet("logs", flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	conversation := set.String("conversation", "", "conversation ID")
	query := set.String("query", "", "full-text query")
	before := set.Int64("before", 0, "cursor log ID")
	limit := set.Int("limit", 100, "maximum logs")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *conversation == "" {
		return fmt.Errorf("--conversation is required")
	}
	path := "/v1/conversations/" + *conversation + "/logs?limit=" + strconv.Itoa(*limit)
	if *query != "" {
		path += "&q=" + url.QueryEscape(*query)
	}
	if *before > 0 {
		path += "&before=" + strconv.FormatInt(*before, 10)
	}
	data, err := newAPIClient(*baseURL, *token).do(http.MethodGet, path, nil)
	if err == nil {
		_, err = os.Stdout.Write(append(data, '\n'))
	}
	return err
}

func cliTools(args []string) error {
	set := flag.NewFlagSet("tools", flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	if err := set.Parse(args); err != nil {
		return err
	}
	return printJSON(newAPIClient(*baseURL, *token).do(http.MethodGet, "/v1/tools", nil))
}

func cliMemory(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("memory action: list, add, delete, consolidate, rebuild")
	}
	set := flag.NewFlagSet("memory "+args[0], flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	content := set.String("content", "", "memory content")
	category := set.String("category", "fact", "memory category")
	id := set.Int64("id", 0, "memory ID")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	client := newAPIClient(*baseURL, *token)
	switch args[0] {
	case "list":
		return printJSON(client.do(http.MethodGet, "/v1/memories", nil))
	case "add":
		if *content == "" {
			return fmt.Errorf("--content is required")
		}
		return printJSON(client.do(http.MethodPost, "/v1/memories", map[string]any{"content": *content, "category": *category}))
	case "delete":
		if *id == 0 {
			return fmt.Errorf("--id is required")
		}
		return printJSON(client.do(http.MethodDelete, "/v1/memories/"+strconv.FormatInt(*id, 10), nil))
	case "consolidate":
		return printJSON(client.do(http.MethodPost, "/v1/memories/consolidate", map[string]any{}))
	case "rebuild":
		return printJSON(client.do(http.MethodPost, "/v1/memories/rebuild", map[string]any{}))
	default:
		return fmt.Errorf("unknown memory action %q", args[0])
	}
}

func cliMCP(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("mcp action: list, connect, disconnect")
	}
	set := flag.NewFlagSet("mcp "+args[0], flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	name := set.String("name", "", "MCP server name")
	transport := set.String("transport", "stdio", "stdio, sse, or streamable_http")
	raw := set.String("config", "{}", "transport config JSON")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	client := newAPIClient(*baseURL, *token)
	switch args[0] {
	case "list":
		return printJSON(client.do(http.MethodGet, "/v1/mcp", nil))
	case "connect":
		if *name == "" || !json.Valid([]byte(*raw)) {
			return fmt.Errorf("--name and valid --config JSON are required")
		}
		return printJSON(client.do(http.MethodPost, "/v1/mcp", mcp.ServerConfig{Name: *name, Transport: *transport, Config: json.RawMessage(*raw)}))
	case "disconnect":
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		return printJSON(client.do(http.MethodDelete, "/v1/mcp/"+*name, nil))
	default:
		return fmt.Errorf("unknown mcp action %q", args[0])
	}
}

func cliDoctor(args []string) error {
	set := flag.NewFlagSet("doctor", flag.ContinueOnError)
	baseURL, token := clientFlags(set)
	configPath := set.String("config", "", "optional config path")
	if err := set.Parse(args); err != nil {
		return err
	}
	executable, _ := os.Executable()
	result := map[string]any{"version": agentCoreVersion, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "executable": filepath.Clean(executable)}
	failed := false
	if *configPath != "" {
		cfg, _, err := loadEffectiveConfig(*configPath)
		if err == nil {
			err = cfg.Validate(config.ModeHTTP)
		}
		result["config"] = map[string]any{"path": *configPath, "valid": err == nil, "error": errorString(err)}
		failed = failed || err != nil
	}
	client := newAPIClient(*baseURL, *token)
	for _, endpoint := range []string{"health", "ready"} {
		data, err := client.do(http.MethodGet, "/"+endpoint, nil)
		var value any
		if err == nil {
			err = json.Unmarshal(data, &value)
		}
		if err != nil {
			value = map[string]string{"error": err.Error()}
			failed = true
		}
		result[endpoint] = value
	}
	if err := writeJSON(result); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("doctor found failures")
	}
	return nil
}

func cliConfig(args []string) error {
	if len(args) == 0 || (args[0] != "validate" && args[0] != "show") {
		return fmt.Errorf("config action: validate or show")
	}
	set := flag.NewFlagSet("config "+args[0], flag.ContinueOnError)
	path := set.String("config", "agent-core.yaml", "config path")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	cfg, _, err := loadEffectiveConfig(*path)
	if err == nil {
		err = cfg.Validate(config.ModeHTTP)
	}
	if err != nil {
		return err
	}
	if args[0] == "validate" {
		fmt.Println("configuration valid")
		return nil
	}
	return writeJSON(cfg.Redacted())
}

func printJSON(data []byte, err error) error {
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return writeJSON(value)
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
