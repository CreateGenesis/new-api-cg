package types

type HeaderRewriteRule struct {
	Remove []string          `json:"remove,omitempty"`
	Set    map[string]string `json:"set,omitempty"`
}

type ChannelHeaderRewriteSettings struct {
	PresetID string `json:"preset_id,omitempty"`
	HeaderRewriteRule
}
