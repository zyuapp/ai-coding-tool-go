package codex

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Read only user-owned config. Codex still validates and merges every config layer itself.
func ReadHomeConfig(home string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	config := map[string]any{}
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return config, nil
}

func table(value any) map[string]any {
	if table, ok := value.(map[string]any); ok {
		return table
	}
	return map[string]any{}
}

// Keep file references anchored to the config that declared them, including custom agent layers.
func PrivateHomeConfig(config map[string]any, source, destination string) []string {
	overrides := []string{}
	for _, key := range []string{"model_instructions_file", "experimental_compact_prompt_file", "model_catalog_json"} {
		if value, ok := config[key].(string); ok && value != "" && !filepath.IsAbs(value) {
			overrides = append(overrides, "-c", key+"="+Toml(filepath.Join(source, value)))
		}
	}
	agentsConfig := table(config["agents"])
	agents := TomlTable{}
	for _, name := range slices.Sorted(maps.Keys(agentsConfig)) {
		if file, ok := table(agentsConfig[name])["config_file"].(string); ok && file != "" && !filepath.IsAbs(file) {
			agents = append(agents, TomlEntry{name, TomlTable{{"config_file", filepath.Join(source, file)}}})
		}
	}
	if len(agents) > 0 {
		overrides = append(overrides, "-c", "agents="+Toml(agents))
	}
	// Trust hashes still refer to the exact same definition. Only the source-path part of the key
	// changes. Never invent a hash, approve a new hook, or write aliases into the user's config.
	hookStates := table(table(config["hooks"])["state"])
	states := TomlTable{}
	separator := string(filepath.Separator)
	for _, key := range slices.Sorted(maps.Keys(hookStates)) {
		// A relocated private home sits inside the source, and its own keys need no alias.
		if !strings.HasPrefix(key, source+separator) || strings.HasPrefix(key, destination+separator) {
			continue
		}
		alias := destination + key[len(source):]
		state := TomlTable{}
		for _, field := range []string{"trusted_hash", "enabled"} {
			switch value := table(hookStates[key])[field].(type) {
			case string, bool:
				state = append(state, TomlEntry{field, value})
			}
		}
		if len(state) > 0 {
			states = append(states, TomlEntry{alias, state})
		}
	}
	// Codex splits the left side of -c on every dot, even inside quotes. Literal hook paths and
	// custom role names must therefore be keys in a TOML value, not dotted override paths.
	if len(states) > 0 {
		overrides = append(overrides, "-c", "hooks.state="+Toml(states))
	}
	return overrides
}

func UsesSharedKeyring(config map[string]any) bool {
	store := config["cli_auth_credentials_store"]
	return store == "keyring" || store == "auto"
}

func EncryptedMcpCredentials(config map[string]any) bool {
	return table(config["features"])["secret_auth_storage"] == true && config["mcp_oauth_credentials_store"] != "file"
}
