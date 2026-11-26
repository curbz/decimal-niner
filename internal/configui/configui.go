package configui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// Server manages configuration edits for one YAML file.
type Server struct {
	configPath string
	webDir     string
	mu         sync.Mutex
	lifecycle  Lifecycle
}

func New(configPath, webDir string) *Server {
	return &Server{configPath: configPath, webDir: webDir}
}

// Start begins a loopback-only HTTP server and returns it for graceful shutdown.
func (s *Server) Start(addr string) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	httpServer := &http.Server{Addr: addr, Handler: s.Handler()}
	go func() {
		if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "config UI server: %v\n", err)
		}
	}()
	return httpServer, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", s.configAPI)
	mux.HandleFunc("/api/files", s.filesAPI)
	mux.HandleFunc("/api/application", s.applicationAPI)
	mux.HandleFunc("/config.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(s.webDir, "config.css"))
	})
	mux.HandleFunc("/config.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(s.webDir, "config.js"))
	})
	mux.HandleFunc("/", s.page)
	return mux
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/config" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(s.webDir, "config.html"))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) configAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if r.Method == http.MethodGet {
		root, err := s.loadNode()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		value, err := nodeValue(root)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": value, "path": s.configPath})
		return
	}

	var payload struct {
		Config map[string]any `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if err := validateConfig(payload.Config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	root, err := s.loadNode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := mergeNode(root, payload.Config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.saveNode(root); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) loadNode() (*yaml.Node, error) {
	data, err := os.ReadFile(s.configPath)
	if os.IsNotExist(err) {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("config is empty")
	}
	return root.Content[0], nil
}

func (s *Server) saveNode(root *yaml.Node) error {
	data, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.configPath); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func nodeValue(n *yaml.Node) (any, error) {
	if n == nil {
		return nil, nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		m := make(map[string]any)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			value, err := nodeValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[key] = value
		}
		return m, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, child := range n.Content {
			value, err := nodeValue(child)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case yaml.ScalarNode:
		var value any
		if err := n.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported YAML node kind")
	}
}

func mergeNode(dst *yaml.Node, src map[string]any) error {
	for key, value := range src {
		idx := mappingIndex(dst, key)
		if idx < 0 {
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			next, err := valueNode(value)
			if err != nil {
				return err
			}
			dst.Content = append(dst.Content, keyNode, next)
			continue
		}
		child := dst.Content[idx+1]
		if next, ok := value.(map[string]any); ok {
			if child.Kind != yaml.MappingNode {
				child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				dst.Content[idx+1] = child
			}
			if err := mergeNode(child, next); err != nil {
				return err
			}
			continue
		}
		updated, err := valueNode(value)
		if err != nil {
			return err
		}
		dst.Content[idx+1] = updated
	}
	return nil
}

func mappingIndex(n *yaml.Node, key string) int {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func valueNode(value any) (*yaml.Node, error) {
	switch v := value.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(v)}, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case float64:
		if v == float64(int64(v)) {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%.0f", v)}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: fmt.Sprintf("%g", v)}, nil
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child, err := valueNode(v[key])
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range v {
			child, err := valueNode(item)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, child)
		}
		return n, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", value)
	}
}

func validateConfig(config map[string]any) error {
	if d9, ok := config["d9"].(map[string]any); ok {
		if engine, exists := d9["traffic_engine"].(string); exists {
			switch engine {
			case "trafficglobal", "vat", "d9traffic":
			default:
				return fmt.Errorf("d9.traffic_engine must be trafficglobal, vat, or d9traffic")
			}
		}
	}
	if xplane, ok := config["xplane_api"].(map[string]any); ok {
		for _, key := range []string{"web_api_http_url", "web_api_websocket_url"} {
			if raw, exists := xplane[key].(string); exists {
				u, err := url.ParseRequestURI(raw)
				if err != nil || u.Scheme == "" {
					return fmt.Errorf("xplane_api.%s must be a valid URL", key)
				}
			}
		}
	}
	if vat, ok := config["vat"].(map[string]any); ok {
		if interval, exists := vat["refresh_interval_seconds"].(float64); exists && interval <= 0 {
			return fmt.Errorf("vat.refresh_interval_seconds must be greater than zero")
		}
	}
	return nil
}

func (s *Server) filesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mode := r.URL.Query().Get("mode")
	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		rawPath = filepath.Dir(s.configPath)
	}
	clean := filepath.Clean(rawPath)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(filepath.Dir(s.configPath), clean)
	}
	entries, err := os.ReadDir(clean)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	type item struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Dir  bool   `json:"dir"`
	}
	result := make([]item, 0, len(entries)+1)
	if parent := filepath.Dir(clean); parent != clean {
		result = append(result, item{Name: "..", Path: parent, Dir: true})
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if mode == "directory" && !info.IsDir() {
			continue
		}
		result = append(result, item{Name: entry.Name(), Path: filepath.Join(clean, entry.Name()), Dir: info.IsDir()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": clean, "entries": result})
}

func (s *Server) applicationAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if s.lifecycle == nil {
			writeJSON(w, http.StatusOK, LifecycleStatus{State: "stopped"})
			return
		}
		writeJSON(w, http.StatusOK, s.lifecycle.Status())
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.lifecycle == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "application lifecycle is unavailable"})
		return
	}
	var action struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	var err error
	switch action.Action {
	case "start":
		err = s.lifecycle.Start()
	case "stop":
		err = s.lifecycle.Stop()
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be start or stop"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.lifecycle.Status())
}

func (s *Server) SetLifecycle(lifecycle Lifecycle) {
	s.lifecycle = lifecycle
}
