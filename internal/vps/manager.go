package vps

// Explicit yaml keys match the lowercase names yaml.v3 used by default,
// so existing config files keep working.
type VPS struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	Host     string   `yaml:"host"`
	User     string   `yaml:"user"`
	Port     int      `yaml:"port"`
	Password string   `yaml:"password,omitempty"`
	KeyPath  string   `yaml:"keypath,omitempty"`
	Group    string   `yaml:"group,omitempty"` // optional section in the fleet view, e.g. "prod"
	Metrics  *Metrics `yaml:"-"`               // runtime only, never saved
}

type Manager struct {
	Servers []VPS
}

func NewManager() *Manager {
	return &Manager{
		Servers: []VPS{},
	}
}

func (m *Manager) AddServer(vps VPS) {
	m.Servers = append(m.Servers, vps)
}
