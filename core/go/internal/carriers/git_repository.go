package carriers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const CarrierGitRepository = "git.repository"

type GitRepositoryConfig struct {
	RemoteURL      string
	WorkDir        string
	WriterID       string
	GitPath        string
	CommandTimeout time.Duration
	// allowLocalFixture permits file:// and scheme-less remotes only in
	// package-owned deterministic unit fixtures. Runtime code cannot set it.
	allowLocalFixture bool
}

type GitRepositoryCarrier struct {
	remoteURL      string
	workDir        string
	writerID       string
	gitPath        string
	commandTimeout time.Duration
	desc           Descriptor
	mu             sync.Mutex
}

type gitRepositoryCursor struct {
	Version int               `json:"v"`
	Writers map[string]string `json:"writers"`
}

func NewGitRepositoryCarrier(cfg GitRepositoryConfig) (*GitRepositoryCarrier, error) {
	remoteURL := strings.TrimSpace(cfg.RemoteURL)
	if remoteURL == "" {
		return nil, fmt.Errorf("git repository: remote_url is required")
	}
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("git repository: invalid remote_url: %w", err)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("git repository: credentials in remote_url are forbidden")
	}
	switch parsed.Scheme {
	case "git":
	case "", "file":
		if !cfg.allowLocalFixture {
			return nil, fmt.Errorf("git repository: local fixture remote scheme %q is forbidden in runtime config", parsed.Scheme)
		}
	default:
		return nil, fmt.Errorf("git repository: unsupported unauthenticated remote scheme %q", parsed.Scheme)
	}
	workDir := strings.TrimSpace(cfg.WorkDir)
	if workDir == "" {
		return nil, fmt.Errorf("git repository: work_dir is required")
	}
	writerID, err := sanitizeGitPathComponent(cfg.WriterID)
	if err != nil {
		return nil, fmt.Errorf("git repository: writer_id: %w", err)
	}
	gitPath := strings.TrimSpace(cfg.GitPath)
	if gitPath == "" {
		gitPath = "git"
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 15 * time.Second
	}
	carrier := &GitRepositoryCarrier{
		remoteURL:      remoteURL,
		workDir:        workDir,
		writerID:       writerID,
		gitPath:        gitPath,
		commandTimeout: cfg.CommandTimeout,
		desc: Descriptor{
			ID:             CarrierGitRepository,
			Provider:       "git",
			Mode:           DeliveryMailbox,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficBulk, fabric.TrafficRepair, fabric.TrafficEgress},
			Capabilities:   []Capability{CapRendezvous, CapMailbox, CapRetained, CapRetrospective, CapList, CapPoll, CapAppendOnly, CapDurable, CapOrdered, CapBulk},
			Limits:         Limits{MaxPayloadBytes: 1 << 20, ChunkPayloadBytes: 256 * 1024},
			Metrics:        Metrics{Healthy: true, Reliability: 0.99, QuotaRemaining: -1},
			Notes:          "Append-only envelope branches in a Git repository; authenticated remotes require a separate provider contract.",
		},
	}
	if err := carrier.ensureClone(context.Background()); err != nil {
		return nil, err
	}
	return carrier, nil
}

func (c *GitRepositoryCarrier) Descriptor() Descriptor { return c.desc }
func (c *GitRepositoryCarrier) IsNative()              {}

func (c *GitRepositoryCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	branch := c.writerBranch(endpoint)
	if err := c.fetchBranch(ctx, branch); err != nil {
		return err
	}
	tip, _ := c.revParse(ctx, "refs/heads/"+branch)
	if tip == "" {
		tip, _ = c.revParse(ctx, "refs/remotes/origin/"+branch)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("git repository: marshal envelope: %w", err)
	}
	if len(raw) > c.desc.Limits.MaxPayloadBytes {
		return fmt.Errorf("git repository: envelope size %d exceeds limit %d", len(raw), c.desc.Limits.MaxPayloadBytes)
	}
	indexFile := filepath.Join(c.workDir, fmt.Sprintf(".wt-index-%s", randomHex(8)))
	defer os.Remove(indexFile)
	env := []string{"GIT_INDEX_FILE=" + indexFile}
	if tip != "" {
		if _, err := c.run(ctx, env, nil, "read-tree", tip); err != nil {
			return err
		}
	}
	blob, err := c.run(ctx, nil, raw, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	messagePath := "envelopes/" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + randomHex(8) + ".json"
	if _, err := c.run(ctx, env, nil, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(blob)+","+messagePath); err != nil {
		return err
	}
	tree, err := c.run(ctx, env, nil, "write-tree")
	if err != nil {
		return err
	}
	commitArgs := []string{"commit-tree", strings.TrimSpace(tree)}
	if tip != "" {
		commitArgs = append(commitArgs, "-p", tip)
	}
	commit, err := c.run(ctx, nil, []byte("WhiteTransport envelope\n"), commitArgs...)
	if err != nil {
		return err
	}
	commit = strings.TrimSpace(commit)
	updateArgs := []string{"update-ref", "refs/heads/" + branch, commit}
	if tip != "" {
		updateArgs = append(updateArgs, tip)
	}
	if _, err := c.run(ctx, nil, nil, updateArgs...); err != nil {
		return err
	}
	if _, err := c.run(ctx, nil, nil, "push", "--porcelain", "origin", "refs/heads/"+branch+":refs/heads/"+branch); err != nil {
		return fmt.Errorf("git repository: append-only push failed: %w", err)
	}
	return nil
}

func (c *GitRepositoryCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := parseGitRepositoryCursor(cursor)
	if err != nil {
		return ReadResult{}, err
	}
	prefix := c.endpointBranchPrefix(endpoint)
	live, err := c.liveWriterRefs(ctx, prefix)
	if err != nil {
		return ReadResult{}, err
	}
	for writer := range state.Writers {
		if live[writer] == "" {
			return ReadResult{}, fmt.Errorf("git repository: previously consumed writer branch %q disappeared", writer)
		}
	}
	if len(live) > 0 {
		refspec := "refs/heads/" + prefix + "/*:refs/remotes/origin/" + prefix + "/*"
		if _, err := c.run(ctx, nil, nil, "fetch", "--no-tags", "--no-prune", "origin", refspec); err != nil {
			return ReadResult{}, err
		}
	}
	writers := make([]string, 0, len(live))
	for writer := range live {
		writers = append(writers, writer)
	}
	sort.Strings(writers)
	var envelopes []fabric.Envelope
	for _, writer := range writers {
		tip := live[writer]
		previous := state.Writers[writer]
		if previous != "" {
			if _, err := c.run(ctx, nil, nil, "merge-base", "--is-ancestor", previous, tip); err != nil {
				return ReadResult{}, fmt.Errorf("git repository: writer branch %q was rewritten: %w", writer, err)
			}
		}
		revision := tip
		if previous != "" {
			revision = previous + ".." + tip
		}
		commitsText, err := c.run(ctx, nil, nil, "rev-list", "--reverse", revision)
		if err != nil {
			return ReadResult{}, err
		}
		for _, commit := range strings.Fields(commitsText) {
			pathsText, err := c.run(ctx, nil, nil, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", commit, "--", "envelopes")
			if err != nil {
				return ReadResult{}, err
			}
			for _, messagePath := range strings.Fields(pathsText) {
				raw, err := c.run(ctx, nil, nil, "show", commit+":"+messagePath)
				if err != nil {
					return ReadResult{}, err
				}
				var envelope fabric.Envelope
				if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
					return ReadResult{}, fmt.Errorf("git repository: malformed envelope at %s:%s: %w", commit, messagePath, err)
				}
				envelopes = append(envelopes, envelope)
			}
		}
		state.Writers[writer] = tip
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return ReadResult{}, fmt.Errorf("git repository: encode cursor: %w", err)
	}
	return ReadResult{Envelopes: envelopes, Cursor: Cursor(encoded)}, nil
}

func (c *GitRepositoryCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	start := time.Now()
	_, err := c.run(ctx, nil, nil, "ls-remote", "--heads", "origin", "refs/heads/"+c.endpointBranchPrefix(endpoint)+"/*")
	if err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	return Metrics{Healthy: true, Latency: time.Since(start), LastOK: time.Now().UTC(), Reliability: 0.99, QuotaRemaining: -1}, nil
}

func (c *GitRepositoryCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *GitRepositoryCarrier) DeleteMessage(context.Context, Endpoint, string) error {
	return fmt.Errorf("git repository: append-only carrier does not delete envelopes")
}

func (c *GitRepositoryCarrier) ensureClone(ctx context.Context) error {
	if info, err := os.Stat(filepath.Join(c.workDir, ".git")); err == nil && info.IsDir() {
		remote, err := c.run(ctx, nil, nil, "remote", "get-url", "origin")
		if err != nil {
			return err
		}
		if strings.TrimSpace(remote) != c.remoteURL {
			return fmt.Errorf("git repository: existing clone origin does not match configured remote")
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.workDir), 0o700); err != nil {
		return fmt.Errorf("git repository: create work parent: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.gitPath, "clone", "--no-checkout", c.remoteURL, c.workDir)
	command.Env = c.commandEnv(nil)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git repository: clone failed: %w: %s", err, safeGitOutput(output))
	}
	if _, err := c.run(ctx, nil, nil, "config", "user.name", "WhiteTransport"); err != nil {
		return err
	}
	if _, err := c.run(ctx, nil, nil, "config", "user.email", "whitetransport@invalid.local"); err != nil {
		return err
	}
	return nil
}

func (c *GitRepositoryCarrier) fetchBranch(ctx context.Context, branch string) error {
	remote, err := c.run(ctx, nil, nil, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(remote) == "" {
		return nil
	}
	_, err = c.run(ctx, nil, nil, "fetch", "--no-tags", "--no-prune", "origin", "refs/heads/"+branch+":refs/remotes/origin/"+branch)
	return err
}

func (c *GitRepositoryCarrier) liveWriterRefs(ctx context.Context, prefix string) (map[string]string, error) {
	output, err := c.run(ctx, nil, nil, "ls-remote", "--heads", "origin", "refs/heads/"+prefix+"/*")
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		writer := strings.TrimPrefix(fields[1], "refs/heads/"+prefix+"/")
		if writer == "" || strings.Contains(writer, "/") {
			return nil, fmt.Errorf("git repository: malformed writer ref %q", fields[1])
		}
		refs[writer] = fields[0]
	}
	return refs, nil
}

func (c *GitRepositoryCarrier) revParse(ctx context.Context, ref string) (string, error) {
	output, err := c.run(ctx, nil, nil, "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (c *GitRepositoryCarrier) endpointBranchPrefix(endpoint Endpoint) string {
	identity := strings.TrimSpace(endpoint.Address)
	if identity == "" {
		identity = strings.TrimSpace(endpoint.ID)
	}
	sum := sha256.Sum256([]byte(identity))
	return "wt/" + hex.EncodeToString(sum[:12])
}

func (c *GitRepositoryCarrier) writerBranch(endpoint Endpoint) string {
	return c.endpointBranchPrefix(endpoint) + "/" + c.writerID
}

func (c *GitRepositoryCarrier) run(parent context.Context, extraEnv []string, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, c.commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.gitPath, append([]string{"-C", c.workDir}, args...)...)
	command.Env = c.commandEnv(extraEnv)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		op := "command"
		if len(args) > 0 {
			op = args[0]
		}
		return "", fmt.Errorf("git repository: %s failed: %w: %s", op, err, safeGitOutput(output))
	}
	return string(output), nil
}

func (c *GitRepositoryCarrier) commandEnv(extra []string) []string {
	env := append([]string(nil), os.Environ()...)
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"LC_ALL=C",
		"GIT_AUTHOR_NAME=WhiteTransport",
		"GIT_AUTHOR_EMAIL=whitetransport@invalid.local",
		"GIT_COMMITTER_NAME=WhiteTransport",
		"GIT_COMMITTER_EMAIL=whitetransport@invalid.local",
	)
	return append(env, extra...)
}

func parseGitRepositoryCursor(cursor Cursor) (gitRepositoryCursor, error) {
	state := gitRepositoryCursor{Version: 1, Writers: make(map[string]string)}
	if strings.TrimSpace(string(cursor)) == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(cursor), &state); err != nil {
		return gitRepositoryCursor{}, fmt.Errorf("git repository: invalid cursor: %w", err)
	}
	if state.Version != 1 || state.Writers == nil {
		return gitRepositoryCursor{}, fmt.Errorf("git repository: unsupported cursor version %d", state.Version)
	}
	return state, nil
}

func sanitizeGitPathComponent(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("value is required")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return "", fmt.Errorf("invalid character %q", char)
	}
	if strings.HasPrefix(value, ".") || strings.Contains(value, "..") {
		return "", errors.New("dot-prefixed and parent-like values are forbidden")
	}
	return value, nil
}

func randomHex(size int) string {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(payload)
}

func safeGitOutput(output []byte) string {
	output = bytes.TrimSpace(output)
	if len(output) > 512 {
		output = output[:512]
	}
	return string(output)
}
