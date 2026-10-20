package vps

import (
	"bytes"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Client struct {
	VPS    VPS
	config *ssh.ClientConfig
	client *ssh.Client
	mu     sync.Mutex // guards client
}

func NewClient(vps VPS) (*Client, error) {
	var auth []ssh.AuthMethod

	if vps.KeyPath != "" {
		path := expandPath(vps.KeyPath)
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, err
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}

	if vps.Password != "" {
		auth = append(auth, ssh.Password(vps.Password))
	}

	if vps.Port == 0 {
		vps.Port = 22
	}

	config := &ssh.ClientConfig{
		User:            vps.User,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback(),
		Timeout:         5 * time.Second,
	}

	return &Client{
		VPS:    vps,
		config: config,
	}, nil
}

func (c *Client) Connect() error {
	addr := fmt.Sprintf("%s:%d", c.VPS.Host, c.VPS.Port)
	client, err := ssh.Dial("tcp", addr, c.config)
	if err != nil {
		return err
	}
	c.client = client
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		err := c.client.Close()
		c.client = nil
		return err
	}
	return nil
}

// newSession opens a session on the shared connection, connecting first if needed.
func (c *Client) newSession() (*ssh.Session, error) {
	// Commands may run concurrently (e.g. tab completion while a command is running)
	c.mu.Lock()
	if c.client == nil {
		if err := c.Connect(); err != nil {
			c.mu.Unlock()
			return nil, err
		}
	}
	conn := c.client
	c.mu.Unlock()

	session, err := conn.NewSession()
	if err != nil {
		// Connection is probably dead (server rebooted, network drop): drop it so the next call reconnects
		c.mu.Lock()
		if c.client == conn {
			conn.Close()
			c.client = nil
		}
		c.mu.Unlock()
		return nil, err
	}
	return session, nil
}

// Download streams a remote file into w. It uses `cat` over SSH, so it works
// without an SFTP subsystem; stderr is kept separate so it can't corrupt the data.
func (c *Client) Download(remotePath string, w io.Writer) error {
	session, err := c.newSession()
	if err != nil {
		return err
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stdout = w
	session.Stderr = &stderr
	if err := session.Run("cat -- " + ShellQuote(remotePath)); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

// Upload streams r into a remote file. The data goes to a temporary ".part" file
// that is renamed only when complete, so an aborted upload never leaves a
// truncated file under the real name.
func (c *Client) Upload(remotePath string, r io.Reader) error {
	session, err := c.newSession()
	if err != nil {
		return err
	}
	defer session.Close()

	part, dst := ShellQuote(remotePath+".vps-fleet-part"), ShellQuote(remotePath)
	var stderr bytes.Buffer
	session.Stdin = r
	session.Stderr = &stderr
	cmd := fmt.Sprintf("cat > %s && mv -f -- %s %s || { rm -f -- %s; exit 1; }", part, part, dst, part)
	if err := session.Run(cmd); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

// ShellQuote quotes s as a single POSIX shell word.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (c *Client) Run(cmd string) (string, error) {
	session, err := c.newSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	out, err := session.CombinedOutput(cmd)
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

func expandPath(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[1:])
	}
	return path
}
