package server

import (
	"agent-core/internal/model"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type wsRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type wsReply struct {
	ID             string             `json:"id,omitempty"`
	Result         any                `json:"result,omitempty"`
	Error          *wsError           `json:"error,omitempty"`
	SubscriptionID string             `json:"subscription_id,omitempty"`
	Event          *model.StreamEvent `json:"event,omitempty"`
}
type wsError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"name": "AgentCoreGo", "protocol": "agentcore-json-v1", "websocket": "/ws", "capabilities": map[string]bool{"http": true, "sse": true, "websocketJson": true, "eventReplay": true, "websocketRpc": false}})
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.websocketOriginAllowed(r) {
		respondError(w, http.StatusForbidden, "origin is not allowed")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	conn.SetReadLimit(s.cfg.Server.MaxRequestBytes)
	ctx, cancel := context.WithCancel(s.backgroundCtx)
	defer cancel()
	defer conn.CloseNow()
	s.backgroundMu.Lock()
	if s.backgroundCtx.Err() != nil {
		s.backgroundMu.Unlock()
		return
	}
	s.sockets[conn] = true
	s.backgroundWG.Add(1)
	s.backgroundMu.Unlock()
	defer func() { s.backgroundMu.Lock(); delete(s.sockets, conn); s.backgroundMu.Unlock(); s.backgroundWG.Done() }()
	if s.cfg.Server.AuthToken != "" && r.Header.Get("Authorization") == "" {
		authCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		var auth wsRequest
		err := wsjson.Read(authCtx, conn, &auth)
		stop()
		var credentials struct {
			Token string `json:"token"`
		}
		if err != nil || auth.Method != "auth" || auth.ID == "" || decodeParams(auth.Params, &credentials) != nil || subtle.ConstantTimeCompare([]byte(credentials.Token), []byte(s.cfg.Server.AuthToken)) != 1 {
			_ = conn.Close(websocket.StatusPolicyViolation, "authentication required")
			return
		}
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		err = wsjson.Write(writeCtx, conn, wsReply{ID: auth.ID, Result: map[string]bool{"authenticated": true}})
		stop()
		if err != nil {
			return
		}
	}
	output := make(chan wsReply, 128)
	var workers sync.WaitGroup
	var subMu sync.Mutex
	subscriptions := map[string]context.CancelFunc{}
	defer func() { cancel(); workers.Wait() }()
	send := func(reply wsReply) bool {
		select {
		case output <- reply:
			return true
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
			cancel()
			_ = conn.CloseNow()
			return false
		}
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer cancel()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case reply := <-output:
				writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := wsjson.Write(writeCtx, conn, reply)
				stop()
				if err != nil {
					_ = conn.CloseNow()
					return
				}
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx)
				stop()
				if err != nil {
					_ = conn.CloseNow()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	slots := make(chan struct{}, 10)
	for {
		var request wsRequest
		if err := wsjson.Read(ctx, conn, &request); err != nil {
			return
		}
		if request.ID == "" || len(request.ID) > 128 {
			send(wsReply{Error: &wsError{Code: 400, Message: "bounded string id is required"}})
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			send(wsReply{ID: request.ID, Error: &wsError{Code: 429, Message: "too many pending requests"}})
			continue
		}
		workers.Add(1)
		go func(request wsRequest) {
			defer workers.Done()
			defer func() { <-slots }()
			reply := wsReply{ID: request.ID}
			switch request.Method {
			case "session.send":
				var params struct {
					CommandID      string            `json:"command_id"`
					ConversationID string            `json:"conversation_id"`
					Message        string            `json:"message"`
					Source         string            `json:"source"`
					Metadata       map[string]string `json:"metadata"`
				}
				err := decodeParams(request.Params, &params)
				if err == nil {
					req := model.ChatRequest{ConversationID: params.ConversationID, Message: params.Message, Source: params.Source, Metadata: params.Metadata}
					var local *commandResult
					req, local, err = s.resolveChat(ctx, req)
					if err == nil {
						if local != nil {
							reply.Result = local
						} else if params.CommandID == "" {
							err = fmt.Errorf("command_id is required for session.send")
						} else {
							reply.Result, err = s.acceptRun(req, params.CommandID)
						}
					}
				}
				if err != nil {
					reply.Error = &wsError{Code: 400, Message: err.Error()}
				}
			case "run.subscribe":
				var params struct {
					RunID          string `json:"run_id"`
					After          uint64 `json:"after"`
					SubscriptionID string `json:"subscription_id"`
				}
				err := decodeParams(request.Params, &params)
				if err != nil || params.RunID == "" || params.SubscriptionID == "" || len(params.SubscriptionID) > 128 {
					reply.Error = &wsError{Code: 400, Message: "run_id and subscription_id are required"}
					break
				}
				if run, err := s.store.GetRun(params.RunID); err != nil || run == nil {
					reply.Error = &wsError{Code: 404, Message: "run not found"}
					break
				}
				subMu.Lock()
				if len(subscriptions) >= 8 || subscriptions[params.SubscriptionID] != nil {
					subMu.Unlock()
					reply.Error = &wsError{Code: 409, Message: "duplicate subscription or subscription limit reached"}
					break
				}
				subCtx, stop := context.WithCancel(ctx)
				subscriptions[params.SubscriptionID] = stop
				subMu.Unlock()
				reply.Result = map[string]any{"subscription_id": params.SubscriptionID}
				if !send(reply) {
					stop()
					return
				}
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer stop()
					defer func() { subMu.Lock(); delete(subscriptions, params.SubscriptionID); subMu.Unlock() }()
					events := make(chan model.StreamEvent, 128)
					done := make(chan struct{})
					go func() {
						defer close(done)
						s.streamRunEvents(r.WithContext(subCtx), params.RunID, params.After, events)
					}()
					defer func() { stop(); <-done }()
					for event := range events {
						if !send(wsReply{SubscriptionID: params.SubscriptionID, Event: &event}) {
							return
						}
					}
				}()
				return
			case "run.unsubscribe":
				var params struct {
					SubscriptionID string `json:"subscription_id"`
				}
				if err := decodeParams(request.Params, &params); err != nil {
					reply.Error = &wsError{Code: 400, Message: err.Error()}
					break
				}
				subMu.Lock()
				stop := subscriptions[params.SubscriptionID]
				subMu.Unlock()
				if stop != nil {
					stop()
				}
				reply.Result = map[string]bool{"unsubscribed": stop != nil}
			default:
				reply.Result, reply.Error = s.websocketHTTP(ctx, request)
			}
			send(reply)
		}(request)
	}
}

func decodeParams(data []byte, target any) error {
	if len(data) == 0 {
		data = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("params must contain one JSON value")
	}
	return nil
}

// This adapter uses the existing HTTP handlers, so permissions, validation and
// session preferences have one implementation across transports.
func (s *Server) websocketHTTP(ctx context.Context, request wsRequest) (any, *wsError) {
	var params struct {
		ID    string          `json:"id"`
		Body  json.RawMessage `json:"body"`
		After uint64          `json:"after"`
	}
	if err := decodeParams(request.Params, &params); err != nil {
		return nil, &wsError{400, err.Error()}
	}
	id := url.PathEscape(params.ID)
	routes := map[string][2]string{
		"server.info": {"GET", "/api/server-info"}, "config.read": {"GET", "/v1/config/effective"},
		"session.list": {"GET", "/v1/conversations"}, "session.read": {"GET", "/v1/conversations/" + id}, "session.messages": {"GET", "/v1/conversations/" + id + "/logs"},
		"run.read": {"GET", "/v1/runs/" + id}, "run.events": {"GET", fmt.Sprintf("/v1/runs/%s/events?after=%d", id, params.After)}, "run.stop": {"POST", "/v1/runs/" + id + "/stop"},
		"permission.list": {"GET", "/v1/approvals"}, "permission.respond": {"POST", "/v1/approvals/" + id}, "input.respond": {"POST", "/v1/chat/" + id + "/clarify"},
		"skill.list": {"GET", "/v1/skills"}, "skill.load": {"POST", "/v1/skills/load"}, "mcp.list": {"GET", "/v1/mcp"},
	}
	route, ok := routes[request.Method]
	if !ok {
		return nil, &wsError{404, "unknown method"}
	}
	if strings.Contains(route[1], "//") || strings.ContainsAny(params.ID, "/\\?#") {
		return nil, &wsError{400, "invalid resource id"}
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(route[0], route[1], bytes.NewReader(params.Body)).WithContext(ctx)
	s.featureMiddleware(s.mux).ServeHTTP(recorder, req)
	var result any
	if json.Unmarshal(recorder.Body.Bytes(), &result) != nil {
		result = recorder.Body.String()
	}
	if recorder.Code >= 400 {
		return nil, &wsError{recorder.Code, recorder.Body.String()}
	}
	return result, nil
}

func (s *Server) websocketOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
			return false
		}
		allowed := strings.EqualFold(parsed.Host, r.Host)
		for _, value := range s.cfg.Server.CORSOrigins {
			if origin == value {
				allowed = true
			}
		}
		if !allowed {
			return false
		}
	}
	if s.cfg.Server.AuthToken == "" {
		host := r.Host
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return false
		}
	}
	return true
}

func (s *Server) CloseConnections() {
	s.backgroundMu.Lock()
	s.backgroundCancel()
	sockets := make([]*websocket.Conn, 0, len(s.sockets))
	for conn := range s.sockets {
		sockets = append(sockets, conn)
	}
	s.backgroundMu.Unlock()
	for _, conn := range sockets {
		_ = conn.CloseNow()
	}
	s.backgroundWG.Wait()
}
