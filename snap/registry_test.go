package snap

import (
	"context"
	"io"
	"testing"
)

// mockDriver is a test StateDriver implementation.
type mockDriver struct {
	name     string
	priority DriverPriority
}

func (d *mockDriver) Name() string                  { return d.name }
func (d *mockDriver) Priority() DriverPriority      { return d.priority }
func (d *mockDriver) Capture(_ context.Context, _ string) (io.ReadCloser, CaptureMetadata, error) {
	return nil, nil, nil
}
func (d *mockDriver) Restore(_ context.Context, _ string, _ io.Reader) error { return nil }
func (d *mockDriver) Verify(_ context.Context, _ string, _ string) (bool, error) {
	return true, nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	d := &mockDriver{name: "test", priority: PriorityEnvironment}
	r.Register(d)

	got, err := r.Get("test")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Name() != "test" {
		t.Errorf("Name = %s, want test", got.Name())
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("nonexistent")
	if err == nil {
		t.Error("Get should fail for unknown driver")
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	d := &mockDriver{name: "dup", priority: PriorityDatabase}
	r.Register(d)

	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	r.Register(d)
}

func TestResolveOrdersByPriority(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockDriver{name: "sqlite", priority: PriorityDatabase})
	r.Register(&mockDriver{name: "dotenv", priority: PriorityEnvironment})
	r.Register(&mockDriver{name: "redis", priority: PriorityDatabase})

	entries := []ConfigEntry{
		{Driver: "sqlite", Source: "./data/app.db"},
		{Driver: "dotenv", Source: "./.env"},
		{Driver: "redis", Source: "redis://localhost:6379"},
	}

	resolved, err := r.Resolve(entries)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(resolved) != 3 {
		t.Fatalf("Resolve returned %d, want 3", len(resolved))
	}

	// dotenv (100) should come before sqlite (200) and redis (200).
	if resolved[0].Driver.Name() != "dotenv" {
		t.Errorf("resolved[0] = %s, want dotenv", resolved[0].Driver.Name())
	}

	// sqlite and redis should preserve config order within same priority.
	if resolved[1].Driver.Name() != "sqlite" {
		t.Errorf("resolved[1] = %s, want sqlite", resolved[1].Driver.Name())
	}
	if resolved[2].Driver.Name() != "redis" {
		t.Errorf("resolved[2] = %s, want redis", resolved[2].Driver.Name())
	}
}

func TestResolveUnknownDriver(t *testing.T) {
	r := NewRegistry()
	entries := []ConfigEntry{
		{Driver: "unknown", Source: "/tmp/foo"},
	}
	_, err := r.Resolve(entries)
	if err == nil {
		t.Error("Resolve should fail for unknown driver")
	}
}
