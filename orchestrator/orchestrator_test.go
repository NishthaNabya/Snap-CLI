package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/NishthaNabya/Snap-CLI/snap"

	// Register drivers
	_ "github.com/NishthaNabya/Snap-CLI/drivers/dotenv"
)

// setupTestRepo creates a real Git repo with a .snap directory for integration testing.
func setupTestRepo(t *testing.T) (string, func()) {
	t.Helper()

	dir, err := os.MkdirTemp("", "snap-orch-test-*")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	// Init a real git repo
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "test")

	// Create an initial commit so HEAD exists
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0o644)
	run("git", "add", ".")
	run("git", "commit", "-m", "initial")

	return dir, cleanup
}

// TestRestoreNoManifest verifies that restoring a commit that has no
// snapshot (e.g., commits made before snap init) exits gracefully
// without errors. This is the core Windows bug scenario.
func TestRestoreNoManifest(t *testing.T) {
	dir, cleanup := setupTestRepo(t)
	defer cleanup()

	orch := New(dir, snap.Registry)
	if err := orch.Init(); err != nil {
		t.Fatal(err)
	}

	// Get HEAD hash
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := string(out[:40])

	// Restore should succeed silently — no manifest exists for this commit
	ctx := context.Background()
	err = orch.Restore(ctx, hash)
	if err != nil {
		t.Errorf("Restore should succeed gracefully when no manifest exists, got: %v", err)
	}
}

// TestRestoreManifestDirMissing verifies graceful behavior when
// the .snap/manifests directory doesn't exist at all.
func TestRestoreManifestDirMissing(t *testing.T) {
	dir, cleanup := setupTestRepo(t)
	defer cleanup()

	orch := New(dir, snap.Registry)
	if err := orch.Init(); err != nil {
		t.Fatal(err)
	}

	// Deliberately remove the manifests directory
	os.RemoveAll(filepath.Join(dir, ".snap", "manifests"))

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, _ := cmd.Output()
	hash := string(out[:40])

	ctx := context.Background()
	err := orch.Restore(ctx, hash)
	if err != nil {
		t.Errorf("Restore should succeed gracefully when manifests dir is missing, got: %v", err)
	}
}

// TestSaveMissingSourceFile verifies that if a configured source file
// doesn't exist (e.g., .env not yet created), Save continues and
// does not fail fatally.
func TestSaveMissingSourceFile(t *testing.T) {
	dir, cleanup := setupTestRepo(t)
	defer cleanup()

	orch := New(dir, snap.Registry)
	if err := orch.Init(); err != nil {
		t.Fatal(err)
	}

	// Write config pointing to a .env file that doesn't exist
	cfg := Config{
		Entries: []snap.ConfigEntry{
			{Driver: "dotenv", Source: ".env"},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(filepath.Join(dir, ".snap", "config.json"), data, 0o644)

	ctx := context.Background()
	// Save should not crash — it should warn and continue
	err := orch.Save(ctx, false)
	// err may be non-nil (partial save) but should NOT panic or crash
	if err != nil {
		// Partial failure is acceptable, fatal crash is not
		t.Logf("Save returned expected partial error: %v", err)
	}
}

// TestSaveAndRestoreRoundTrip verifies the full happy path:
// Save captures a .env file, and Restore puts it back.
func TestSaveAndRestoreRoundTrip(t *testing.T) {
	dir, cleanup := setupTestRepo(t)
	defer cleanup()

	orch := New(dir, snap.Registry)
	if err := orch.Init(); err != nil {
		t.Fatal(err)
	}

	// Create a .env file
	envPath := filepath.Join(dir, ".env")
	os.WriteFile(envPath, []byte("SECRET=hello123\n"), 0o644)

	// Write config
	cfg := Config{
		Entries: []snap.ConfigEntry{
			{Driver: "dotenv", Source: ".env"},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(filepath.Join(dir, ".snap", "config.json"), data, 0o644)

	ctx := context.Background()

	// Save
	if err := orch.Save(ctx, false); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Get the hash
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, _ := cmd.Output()
	hash := string(out[:40])

	// Delete the .env file (simulate checkout to different state)
	os.Remove(envPath)

	// Restore
	if err := orch.Restore(ctx, hash); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// Verify .env was restored
	restored, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("Expected .env to be restored, got: %v", err)
	}
	if string(restored) != "SECRET=hello123\n" {
		t.Errorf("Restored content = %q, want %q", string(restored), "SECRET=hello123\n")
	}
}

// TestSaveEmptyConfig verifies that Save with no configured drivers
// exits cleanly without errors.
func TestSaveEmptyConfig(t *testing.T) {
	dir, cleanup := setupTestRepo(t)
	defer cleanup()

	orch := New(dir, snap.Registry)
	if err := orch.Init(); err != nil {
		t.Fatal(err)
	}

	// Config has empty entries (default after snap init)
	ctx := context.Background()
	err := orch.Save(ctx, false)
	if err != nil {
		t.Errorf("Save with empty config should succeed, got: %v", err)
	}
}
