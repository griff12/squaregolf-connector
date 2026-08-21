package plugin

// FieldType is the input kind for a plugin config field, so the frontend can
// render the right control without knowing the plugin.
type FieldType string

const (
	FieldText   FieldType = "text"
	FieldNumber FieldType = "number"
	FieldToggle FieldType = "toggle"
)

// ConfigField declares one user-editable setting a plugin exposes.
type ConfigField struct {
	Key   string    `json:"key"`
	Label string    `json:"label"`
	Type  FieldType `json:"type"`
	Help  string    `json:"help,omitempty"`
}

// Manifest is a plugin's self-description. The frontend renders integrations
// entirely from manifests + live status, so adding a plugin needs no UI changes.
type Manifest struct {
	Name         string        `json:"name"`
	DisplayName  string        `json:"displayName"`
	Icon         string        `json:"icon,omitempty"` // Material icon name
	ConfigSchema []ConfigField `json:"configSchema,omitempty"`
}

// Describable is implemented by plugins that advertise a manifest for the UI.
type Describable interface {
	Describe() Manifest
}

// ConfigStore is implemented by plugins with user-editable configuration. Values
// are a free-form map keyed by the manifest's ConfigField keys; JSON numbers
// arrive as float64.
type ConfigStore interface {
	Config() map[string]any
	Configure(values map[string]any)
}
