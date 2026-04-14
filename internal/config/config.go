package config

// Config represents the clotilde configuration.
type Config struct {
	// Defaults are applied to all sessions unless overridden
	Defaults Defaults `json:"defaults,omitempty" toml:"defaults,omitempty"`
	// Profiles is a map of named session profiles
	Profiles map[string]Profile `json:"profiles,omitempty" toml:"profiles,omitempty"`
	// Search configures the conversation search LLM backend
	Search SearchConfig `json:"search,omitempty" toml:"search,omitempty"`
}

// SearchConfig configures the LLM backend for conversation search.
type SearchConfig struct {
	// Backend is "claude" (default) or "local"
	Backend string       `json:"backend,omitempty" toml:"backend,omitempty"`
	Local   SearchLocal  `json:"local,omitempty" toml:"local,omitempty"`
	Claude  SearchClaude `json:"claude,omitempty" toml:"claude,omitempty"`
}

// SearchLocal configures a local OpenAI-compatible LLM endpoint.
type SearchLocal struct {
	URL              string        `json:"url,omitempty" toml:"url,omitempty"`
	Token            string        `json:"token,omitempty" toml:"token,omitempty"`
	Model            string        `json:"model,omitempty" toml:"model,omitempty"`
	RerankModel      string        `json:"rerankModel,omitempty" toml:"rerank_model,omitempty"`
	DeepModel        string        `json:"deepModel,omitempty" toml:"deep_model,omitempty"`
	Pipeline         []SearchLayer `json:"pipeline,omitempty" toml:"pipeline,omitempty"`
	Temperature      float64       `json:"temperature" toml:"temperature"`
	TopP             float64       `json:"topP" toml:"top_p"`
	FrequencyPenalty float64       `json:"frequencyPenalty" toml:"frequency_penalty"`
	MaxConcurrent    int           `json:"maxConcurrent,omitempty" toml:"max_concurrent,omitempty"`
	ChunkSize        int           `json:"chunkSize,omitempty" toml:"chunk_size,omitempty"`
	MaxMemoryGB      int           `json:"maxMemoryGB,omitempty" toml:"max_memory_gb,omitempty"`
	ContextLength    int           `json:"contextLength,omitempty" toml:"context_length,omitempty"`
}

// SearchLayer defines one stage of the search pipeline.
type SearchLayer struct {
	Name  string `json:"name" toml:"name"`   // "sweep", "rerank", "deep"
	Model string `json:"model" toml:"model"` // model to use for this layer
}

// ResolvePipeline returns the search pipeline for a given depth.
// Depth levels: "quick" (sweep only), "normal" (sweep + rerank), "deep" (all layers).
func (s SearchLocal) ResolvePipeline(depth string) []SearchLayer {
	// If explicit pipeline is configured, use it up to the requested depth
	if len(s.Pipeline) > 0 {
		switch depth {
		case "quick":
			if len(s.Pipeline) >= 1 {
				return s.Pipeline[:1]
			}
		case "deep":
			return s.Pipeline
		default: // "normal"
			if len(s.Pipeline) >= 2 {
				return s.Pipeline[:2]
			}
			return s.Pipeline
		}
		return s.Pipeline
	}

	// Fall back to individual model fields
	var layers []SearchLayer
	model := s.Model
	if model == "" {
		model = "qwen2.5-coder-32b"
	}
	layers = append(layers, SearchLayer{Name: "sweep", Model: model})

	if depth == "quick" {
		return layers
	}

	if s.RerankModel != "" {
		layers = append(layers, SearchLayer{Name: "rerank", Model: s.RerankModel})
	}

	if depth == "deep" && s.DeepModel != "" {
		layers = append(layers, SearchLayer{Name: "deep", Model: s.DeepModel})
	}

	return layers
}

// SearchClaude configures the Claude backend for search.
type SearchClaude struct {
	Model string `json:"model,omitempty" toml:"model,omitempty"`
}

// Defaults are session defaults applied to all sessions.
type Defaults struct {
	RemoteControl bool   `json:"remoteControl,omitempty" toml:"remote_control,omitempty"`
	Model         string `json:"model,omitempty" toml:"model,omitempty"`
	EffortLevel   string `json:"effortLevel,omitempty" toml:"effort_level,omitempty"`
}

// Profile represents a named preset of session settings.
type Profile struct {
	Model          string       `json:"model,omitempty" toml:"model,omitempty"`
	PermissionMode string       `json:"permissionMode,omitempty" toml:"permission_mode,omitempty"`
	Permissions    *Permissions `json:"permissions,omitempty" toml:"permissions,omitempty"`
	OutputStyle    string       `json:"outputStyle,omitempty" toml:"output_style,omitempty"`
}

// Permissions represents the permissions configuration for sessions.
// Kept in config package to avoid circular imports with session package.
type Permissions struct {
	Allow                        []string `json:"allow,omitempty" toml:"allow,omitempty"`
	Ask                          []string `json:"ask,omitempty" toml:"ask,omitempty"`
	Deny                         []string `json:"deny,omitempty" toml:"deny,omitempty"`
	AdditionalDirectories        []string `json:"additionalDirectories,omitempty" toml:"additional_directories,omitempty"`
	DefaultMode                  string   `json:"defaultMode,omitempty" toml:"default_mode,omitempty"`
	DisableBypassPermissionsMode string   `json:"disableBypassPermissionsMode,omitempty" toml:"disable_bypass_permissions_mode,omitempty"`
}

// NewConfig creates a new Config with sensible defaults.
func NewConfig() *Config {
	return &Config{
		Profiles: make(map[string]Profile),
	}
}
