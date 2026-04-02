// Package orchestrator coordinates the save and restore workflows.
// It ties together the Driver Registry, CAS, Manifest Manager,
// and file-system locking into coherent operations.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NishthaNabya/Snap-CLI/cas"
	"github.com/NishthaNabya/Snap-CLI/lock"
	"github.com/NishthaNabya/Snap-CLI/manifest"
	"github.com/NishthaNabya/Snap-CLI/snap"
)

const (
	snapDir      = ".snap"
	objectsDir   = "objects"
	manifestsDir = "manifests"
	tmpDir       = "tmp"
	lockFile     = "snap.lock"
	configFile   = "config.json"
)

// Config represents the .snap/config.json file.
type Config struct {
	Entries []snap.ConfigEntry `json:"entries"`
}

// Orchestrator is the central coordinator for Snap operations.
type Orchestrator struct {
	root     string // repo root
	snapPath string // .snap/ absolute path
	store    *cas.Store
	manifMgr *manifest.Manager
	registry *snap.DriverRegistry
}

// New creates an Orchestrator for the repository at root.
func New(root string, registry *snap.DriverRegistry) *Orchestrator {
	sp := filepath.Join(root, snapDir)
	return &Orchestrator{
		root:     root,
		snapPath: sp,
		store:    cas.NewStore(filepath.Join(sp, objectsDir), filepath.Join(sp, tmpDir)),
		manifMgr: manifest.NewManager(filepath.Join(sp, manifestsDir), filepath.Join(sp, tmpDir)),
		registry: registry,
	}
}

// Init creates the .snap directory structure and config file.
func (o *Orchestrator) Init() error {
	dirs := []string{
		o.snapPath,
		filepath.Join(o.snapPath, objectsDir),
		filepath.Join(o.snapPath, manifestsDir),
		filepath.Join(o.snapPath, tmpDir),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("init: mkdir %s: %w", d, err)
		}
	}

	// Write default config if it doesn't exist.
	cfgPath := filepath.Join(o.snapPath, configFile)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		cfg := Config{
			Entries: []snap.ConfigEntry{},
		}
		data, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
			return fmt.Errorf("init: write config: %w", err)
		}
	}

	return nil
}

// Save captures the current system state and binds it to HEAD.
// If force is false and a manifest already exists for HEAD, this is a no-op.
func (o *Orchestrator) Save(ctx context.Context, force bool) error {
	// Step 1: Acquire exclusive lock.
	lk, err := lock.Acquire(filepath.Join(o.snapPath, lockFile))
	if err != nil {
		return err
	}
	defer lk.Release()

	// Cleanup orphaned temp files.
	o.store.CleanupOrphans(1 * time.Hour)

	// Step 2: Resolve Git HEAD.
	gitHash, err := resolveHEAD(ctx, o.root)
	if err != nil {
		return err
	}

	// Step 3: Idempotency check.
	if !force && o.manifMgr.Exists(gitHash) {
		fmt.Fprintf(os.Stderr, "snap: manifest already exists for %s (use --force to overwrite)\n", gitHash[:12])
		return nil
	}

	// Step 4: Load config and resolve drivers in priority order.
	cfg, err := o.loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Entries) == 0 {
		fmt.Fprintln(os.Stderr, "snap: no drivers configured in .snap/config.json")
		return nil
	}

	resolved, err := o.registry.Resolve(cfg.Entries)
	if err != nil {
		return err
	}

	// Step 5: Capture each source in priority order.
	mf := manifest.New(gitHash)
	var saveErrors []string

	for _, rd := range resolved {
		fmt.Fprintf(os.Stderr, "snap: capturing %s (%s)\n", rd.Source, rd.Driver.Name())

		rc, meta, err := rd.Driver.Capture(ctx, rd.Source)
		if err != nil {
			saveErrors = append(saveErrors, fmt.Sprintf("%s: %v", rd.Source, err))
			continue // Prevent one missing file from aborting the whole save
		}

		// Step 6: Stream to CAS (hash-while-writing).
		blobHash, blobSize, err := o.store.Put(rc)
		rc.Close()
		if err != nil {
			saveErrors = append(saveErrors, fmt.Sprintf("%s: store error: %v", rd.Source, err))
			continue
		}

		if o.store.Has(blobHash) {
			fmt.Fprintf(os.Stderr, "snap:   → %s (%d bytes, dedup: hit)\n", blobHash[:12], blobSize)
		} else {
			fmt.Fprintf(os.Stderr, "snap:   → %s (%d bytes)\n", blobHash[:12], blobSize)
		}

		mf.AddEntry(rd.Driver.Name(), rd.Source, blobHash, blobSize, meta)
	}

	// Step 7: Seal and write manifest atomically.
	if err := mf.Seal(); err != nil {
		return fmt.Errorf("snap: seal manifest: %w", err)
	}
	
	// Only persist the manifest if we successfully captured at least one file
	if len(mf.Entries) > 0 {
		if err := o.manifMgr.Write(mf); err != nil {
			return fmt.Errorf("snap: write manifest: %w", err)
		}
	}

	fmt.Fprintf(os.Stderr, "snap: saved state for %s (%d entries)\n", gitHash[:12], len(mf.Entries))
	
	if len(saveErrors) > 0 {
		return fmt.Errorf("partial save failure:\n  %s", strings.Join(saveErrors, "\n  "))
	}
	
	return nil
}

// Restore loads the manifest for the given git hash and restores
// all captured state in priority order.
func (o *Orchestrator) Restore(ctx context.Context, gitHash string) error {
	// Step 1: Acquire exclusive lock.
	lk, err := lock.Acquire(filepath.Join(o.snapPath, lockFile))
	if err != nil {
		return err
	}
	defer lk.Release()

	// Step 2: Load manifest.
	mf, err := o.manifMgr.Load(gitHash)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "snap: warning: no snapshot for commit %s\n", gitHash[:12])
			return nil
		}
		return err
	}

	// Step 3: Sort entries by driver priority.
	entries := make([]snap.ConfigEntry, len(mf.Entries))
	for i, e := range mf.Entries {
		entries[i] = snap.ConfigEntry{Driver: e.Driver, Source: e.Source}
	}
	resolved, err := o.registry.Resolve(entries)
	if err != nil {
		return err
	}

	// Build a lookup from (driver, source) → manifest entry.
	entryMap := make(map[string]manifest.Entry)
	for _, e := range mf.Entries {
		key := e.Driver + ":" + e.Source
		entryMap[key] = e
	}

	// Step 4: Restore in priority order.
	var restoreErrors []string
	for _, rd := range resolved {
		key := rd.Driver.Name() + ":" + rd.Source
		entry, ok := entryMap[key]
		if !ok {
			continue
		}

		fmt.Fprintf(os.Stderr, "snap: restoring %s (%s)\n", rd.Source, rd.Driver.Name())

		// Verify blob integrity before restoring.
		ok, err := o.store.Verify(entry.BlobHash)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("%s: verify failed: %v", rd.Source, err))
			continue
		}
		if !ok {
			restoreErrors = append(restoreErrors, fmt.Sprintf("%s: blob corrupted (hash mismatch)", rd.Source))
			continue
		}

		// Open blob and pass to driver.
		rc, err := o.store.Get(entry.BlobHash)
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("%s: %v", rd.Source, err))
			continue
		}

		if err := rd.Driver.Restore(ctx, rd.Source, rc); err != nil {
			rc.Close()
			restoreErrors = append(restoreErrors, fmt.Sprintf("%s: %v", rd.Source, err))
			continue
		}
		rc.Close()

		fmt.Fprintf(os.Stderr, "snap:   ✓ restored %s\n", rd.Source)
	}

	if len(restoreErrors) > 0 {
		return fmt.Errorf("snap: partial restore failure:\n  %s", strings.Join(restoreErrors, "\n  "))
	}

	fmt.Fprintf(os.Stderr, "snap: restored state for %s (%d entries)\n", gitHash[:12], len(mf.Entries))
	return nil
}

// loadConfig reads .snap/config.json.
func (o *Orchestrator) loadConfig() (*Config, error) {
	cfgPath := filepath.Join(o.snapPath, configFile)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("orchestrator: parse config: %w", err)
	}
	return &cfg, nil
}

// resolveHEAD returns the full SHA-1 hash of the current Git HEAD.
func resolveHEAD(ctx context.Context, repoRoot string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("orchestrator: git rev-parse HEAD: %w", err)
	}
	hash := strings.TrimSpace(string(out))
	if len(hash) != 40 {
		return "", fmt.Errorf("orchestrator: unexpected git hash length: %d", len(hash))
	}
	return hash, nil
}

// SnapPath returns the absolute path to the .snap directory.
func (o *Orchestrator) SnapPath() string {
	return o.snapPath
}

// StoreRef returns a reference to the CAS store (for CLI commands).
func (o *Orchestrator) StoreRef() *cas.Store {
	return o.store
}

// ManifestRef returns a reference to the manifest manager.
func (o *Orchestrator) ManifestRef() *manifest.Manager {
	return o.manifMgr
}

// VerifyAll checks integrity of all blobs referenced in the manifest
// for the given git hash.
func (o *Orchestrator) VerifyAll(ctx context.Context, gitHash string) error {
	mf, err := o.manifMgr.Load(gitHash)
	if err != nil {
		return err
	}

	allOK := true
	for _, entry := range mf.Entries {
		ok, err := o.store.Verify(entry.BlobHash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "snap: FAIL %s (%s): %v\n", entry.Source, entry.BlobHash[:12], err)
			allOK = false
			continue
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "snap: CORRUPT %s (%s)\n", entry.Source, entry.BlobHash[:12])
			allOK = false
		} else {
			fmt.Fprintf(os.Stderr, "snap: OK %s (%s)\n", entry.Source, entry.BlobHash[:12])
		}
	}

	if !allOK {
		return fmt.Errorf("snap: integrity check failed")
	}
	return nil
}
