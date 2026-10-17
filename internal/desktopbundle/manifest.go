package desktopbundle

import "errors"

// This fixed shape implements the binary-server subset of MCPB manifest 0.3.
// https://github.com/modelcontextprotocol/mcpb/blob/main/MANIFEST.md
type manifest struct {
	ManifestVersion string `json:"manifest_version"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Version         string `json:"version"`
	Description     string `json:"description"`
	LongDescription string `json:"long_description"`
	Author          struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"author"`
	Server struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
		MCPConfig  struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcp_config"`
	} `json:"server"`
	Compatibility struct {
		Platforms []string `json:"platforms"`
	} `json:"compatibility"`
	UserConfig     map[string]directoryConfig `json:"user_config"`
	License        string                     `json:"license"`
	Homepage       string                     `json:"homepage"`
	Support        string                     `json:"support"`
	ToolsGenerated bool                       `json:"tools_generated"`
	Meta           map[string]targetMetadata  `json:"_meta"`
}

type directoryConfig struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default"`
}

type targetMetadata struct {
	GOOS                     string `json:"goos"`
	GOARCH                   string `json:"goarch"`
	NativeValidationRequired bool   `json:"native_validation_required"`
	DesktopNativeSupported   bool   `json:"desktop_native_supported"`
}

func newManifest(version, goos, goarch string) (manifest, error) {
	var m manifest
	if (goos != "darwin" && goos != "linux") || (goarch != "amd64" && goarch != "arm64") {
		return m, errors.New("bundle platform must be darwin or linux with amd64 or arm64 architecture")
	}
	m.ManifestVersion, m.Name, m.Version = "0.3", "questlock", version
	m.DisplayName = "Questlock (" + goos + "/" + goarch + ")"
	m.Description = "Run bounded local Questlock tasks without provider credentials."
	m.LongDescription = "Built for " + goos + "/" + goarch + ". Questlock runs a local broker and deterministic tasks in the request workspace you choose. The state directory stores private local credentials and service state. Install this bundle through Claude Desktop, review the directory settings, and verify its connector before use. Creating the bundle does not validate it in the native application."
	m.Author.Name, m.Author.URL = "spfuzzylink contributors", "https://github.com/spfuzzylink"
	m.Server.Type, m.Server.EntryPoint = "binary", "server/questlock"
	m.Server.MCPConfig.Command = "${__dirname}/server/questlock"
	m.Server.MCPConfig.Args = []string{"worker", "local-serve", "--client", "claude-desktop", "--state-dir", "${user_config.state_dir}", "--workspace", "${user_config.workspace}"}
	m.Compatibility.Platforms = []string{goos}
	m.UserConfig = map[string]directoryConfig{
		"state_dir": {Type: "directory", Title: "Private Questlock state", Description: "Choose an absolute directory for the local broker, private credential, and service state.", Required: true, Default: "${HOME}/.local/share/questlock/local"},
		"workspace": {Type: "directory", Title: "Request workspace", Description: "Choose an absolute directory containing the request files Questlock may process.", Required: true, Default: "${HOME}/.local/share/questlock/requests"},
	}
	m.License, m.Homepage, m.Support = "MIT", "https://github.com/spfuzzylink/questlock", "https://github.com/spfuzzylink/questlock/issues"
	m.ToolsGenerated = true
	// MCPB 0.3 has no architecture constraint. Keep the exact target visible
	// and machine-readable without inventing an unsupported compatibility key.
	m.Meta = map[string]targetMetadata{"io.github.spfuzzylink.questlock": {GOOS: goos, GOARCH: goarch, NativeValidationRequired: true, DesktopNativeSupported: goos == "darwin"}}
	return m, nil
}
