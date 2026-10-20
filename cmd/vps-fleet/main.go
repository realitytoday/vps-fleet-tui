package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"vps-fleet-tui/internal/config"
	"vps-fleet-tui/internal/ui"
	"vps-fleet-tui/internal/vps"
)

func main() {
	var profile string
	flag.StringVar(&profile, "p", config.DefaultProfile, "server profile to open (config.yaml is \"default\", others are in "+config.ProfilesDir()+")")
	flag.StringVar(&profile, "profile", config.DefaultProfile, "same as -p")
	flag.Parse()

	cfg, err := config.LoadProfile(profile)
	if err != nil {
		fmt.Printf("Error loading profile: %v\n", err)
		os.Exit(1)
	}

	manager := vps.NewManager()
	if len(cfg.Servers) > 0 {
		manager.Servers = cfg.Servers
	} else if profile == config.DefaultProfile {
		// Fallback to example if no config exists
		manager.AddServer(vps.VPS{ID: "1", Name: "Web Server", Host: "192.168.1.10"})
		manager.AddServer(vps.VPS{ID: "2", Name: "DB Server", Host: "192.168.1.11"})

		// Create initial config file
		config.SaveConfig(&config.Config{Servers: manager.Servers})
	}

	settings, err := config.LoadSettings()
	if err != nil {
		fmt.Printf("Error loading settings (using defaults): %v\n", err)
	}

	model := ui.NewModel(manager).WithSettings(settings).WithProfile(profile)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
		os.Exit(1)
	}
}
