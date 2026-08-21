package plugin

import (
	"context"
	"log"
)

// Registry holds the plugins assembled by the composition root and lets the rest
// of the engine drive them generically by name. The engine depends on the
// Registry; only the composition root (main) knows the concrete plugin types.
type Registry struct {
	host     Host
	timeline *Timeline
	plugins  map[string]Plugin
	order    []string
}

// NewRegistry creates an empty registry bound to a host.
func NewRegistry(host Host) *Registry {
	timeline := NewTimeline()
	if provider, ok := host.(interface{ Timeline() *Timeline }); ok && provider.Timeline() != nil {
		timeline = provider.Timeline()
	}
	return &Registry{host: host, timeline: timeline, plugins: map[string]Plugin{}}
}

// Timeline returns the canonical shot/result timeline shared by every plugin.
func (r *Registry) Timeline() *Timeline { return r.timeline }

// Register adds a plugin. The last registration for a name wins.
func (r *Registry) Register(p Plugin) {
	name := p.Name()
	if _, exists := r.plugins[name]; !exists {
		r.order = append(r.order, name)
	}
	r.plugins[name] = p
}

// Get returns a plugin by name.
func (r *Registry) Get(name string) (Plugin, bool) {
	p, ok := r.plugins[name]
	return p, ok
}

// Names returns the registered plugin names in registration order.
func (r *Registry) Names() []string {
	names := make([]string, len(r.order))
	copy(names, r.order)
	return names
}

// StartAll starts every registered plugin with the host.
func (r *Registry) StartAll(ctx context.Context) {
	for _, name := range r.order {
		if err := r.plugins[name].Start(ctx, r.host); err != nil {
			log.Printf("plugin %q failed to start: %v", name, err)
		}
	}
}

// StopAll stops every registered plugin.
func (r *Registry) StopAll() {
	for _, name := range r.order {
		if err := r.plugins[name].Stop(); err != nil {
			log.Printf("plugin %q failed to stop: %v", name, err)
		}
	}
}

// Connectable returns the plugin as a Connectable, if it supports it.
func (r *Registry) Connectable(name string) (Connectable, bool) {
	p, ok := r.plugins[name]
	if !ok {
		return nil, false
	}
	c, ok := p.(Connectable)
	return c, ok
}

func (r *Registry) ConnectionLifecycle(name string) (ConnectionLifecycle, bool) {
	p, ok := r.plugins[name]
	if !ok {
		return nil, false
	}
	capability, ok := p.(ConnectionLifecycle)
	return capability, ok
}

func (r *Registry) Actionable(name string) (Actionable, bool) {
	p, ok := r.plugins[name]
	if !ok {
		return nil, false
	}
	capability, ok := p.(Actionable)
	return capability, ok
}

// ConfigStore returns the plugin as a ConfigStore, if it supports it.
func (r *Registry) ConfigStore(name string) (ConfigStore, bool) {
	p, ok := r.plugins[name]
	if !ok {
		return nil, false
	}
	c, ok := p.(ConfigStore)
	return c, ok
}

// View is a single integration's full state for the generic API: its manifest,
// its capability flags, its current config values, and its live status.
type View struct {
	Manifest
	Connectable  bool           `json:"connectable"`
	Actionable   bool           `json:"actionable"`
	Configvalues map[string]any `json:"config,omitempty"`
}

// Views returns one View per registered plugin (status is layered on by the
// caller, which owns the status state). Plugins that do not describe themselves
// are skipped.
func (r *Registry) Views() []View {
	views := make([]View, 0, len(r.order))
	for _, name := range r.order {
		p := r.plugins[name]
		d, ok := p.(Describable)
		if !ok {
			continue
		}
		view := View{Manifest: d.Describe()}
		if _, ok := p.(Connectable); ok {
			view.Connectable = true
		}
		if _, ok := p.(ConnectionLifecycle); ok {
			view.Connectable = true
		}
		if _, ok := p.(Actionable); ok {
			view.Actionable = true
		}
		if cs, ok := p.(ConfigStore); ok {
			view.Configvalues = cs.Config()
		}
		views = append(views, view)
	}
	return views
}
