package plugin

import (
	"context"
	"log"
)

// Registry holds the plugins assembled by the composition root and lets the rest
// of the engine drive them generically by name. The engine depends on the
// Registry; only the composition root (main) knows the concrete plugin types.
type Registry struct {
	host    Host
	plugins map[string]Plugin
	order   []string
}

// NewRegistry creates an empty registry bound to a host.
func NewRegistry(host Host) *Registry {
	return &Registry{host: host, plugins: map[string]Plugin{}}
}

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

// Configurable returns the plugin as a Configurable, if it supports it.
func (r *Registry) Configurable(name string) (Configurable, bool) {
	p, ok := r.plugins[name]
	if !ok {
		return nil, false
	}
	c, ok := p.(Configurable)
	return c, ok
}
