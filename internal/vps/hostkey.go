package vps

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var hostKeyMu sync.Mutex

// KnownHostsPath is vps-fleet's own known_hosts file, next to config.yaml.
func KnownHostsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vps-fleet", "known_hosts")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hostKeyCallback verifies server keys against ~/.ssh/known_hosts and
// vps-fleet's own known_hosts. A host seen for the first time is remembered
// (like ssh's StrictHostKeyChecking=accept-new); a host whose key changed is
// refused, because that is what a man-in-the-middle looks like.
func hostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		hostKeyMu.Lock()
		defer hostKeyMu.Unlock()

		own := KnownHostsPath()
		var files []string
		if home, err := os.UserHomeDir(); err == nil {
			if f := filepath.Join(home, ".ssh", "known_hosts"); fileExists(f) {
				files = append(files, f)
			}
		}
		if fileExists(own) {
			files = append(files, own)
		}

		if len(files) > 0 {
			check, err := knownhosts.New(files...)
			if err != nil {
				return fmt.Errorf("reading known_hosts: %w", err)
			}
			err = check(hostname, remote, key)
			if err == nil {
				return nil
			}
			var keyErr *knownhosts.KeyError
			if !errors.As(err, &keyErr) || len(keyErr.Want) > 0 {
				return fmt.Errorf("HOST KEY MISMATCH for %s (possible man-in-the-middle; if the server was reinstalled, remove its line from %s): %w", hostname, own, err)
			}
		}

		// Unknown host: trust on first use.
		if err := os.MkdirAll(filepath.Dir(own), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(own, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
		return err
	}
}
