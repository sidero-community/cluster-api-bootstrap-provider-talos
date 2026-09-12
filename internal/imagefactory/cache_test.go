package imagefactory

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeAPI struct {
	versions   []string
	extensions map[string][]string
	ids        map[string]string
	err        error
	calls      map[string]int
}

func (f *fakeAPI) count(name string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeAPI) Versions(context.Context) ([]string, error) {
	f.count("versions")
	return f.versions, f.err
}

func (f *fakeAPI) OfficialExtensions(_ context.Context, version string) ([]string, error) {
	f.count("extensions")
	return f.extensions[version], f.err
}

func (f *fakeAPI) CreateSchematic(_ context.Context, s Schematic) (string, error) {
	f.count("create")
	if f.err != nil {
		return "", f.err
	}
	body, _ := s.Marshal()
	return f.ids[string(body)], nil
}

func (f *fakeAPI) InstallerImage(id, version string) string {
	return "factory.example.test/metal-installer/" + id + ":" + version
}

func TestCachedReusesWithinTTLAndServesStaleOnError(t *testing.T) {
	s := Schematic{Customization: Customization{SystemExtensions: SystemExtensions{OfficialExtensions: []string{"a/b"}}}}
	body, _ := s.Marshal()
	f := &fakeAPI{versions: []string{"v1.14.0"}, extensions: map[string][]string{"v1.14.0": {"a/b"}}, ids: map[string]string{string(body): "id1"}}
	c := NewCached(f, time.Hour)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if v, err := c.Versions(ctx); err != nil || len(v) != 1 {
			t.Fatal(v, err)
		}
		if e, err := c.OfficialExtensions(ctx, "v1.14.0"); err != nil || len(e) != 1 {
			t.Fatal(e, err)
		}
		if id, err := c.CreateSchematic(ctx, s); err != nil || id != "id1" {
			t.Fatal(id, err)
		}
	}
	if f.calls["versions"] != 1 || f.calls["extensions"] != 1 || f.calls["create"] != 1 {
		t.Fatalf("expected one upstream call each, got %v", f.calls)
	}

	f.err = errors.New("factory down")
	if v, err := c.Versions(ctx); err != nil || len(v) != 1 {
		t.Fatalf("stale versions must be served on error, got %v, %v", v, err)
	}
	if _, err := c.OfficialExtensions(ctx, "v1.15.0"); err == nil {
		t.Fatal("an uncached key has nothing stale to serve")
	}
	if id, err := c.CreateSchematic(ctx, s); err != nil || id != "id1" {
		t.Fatalf("schematic ids are content addressed and never expire, got %q, %v", id, err)
	}
	if c.InstallerImage("x", "v1") != "factory.example.test/metal-installer/x:v1" {
		t.Fatal("InstallerImage must pass through")
	}
}

func TestCachedExpiresLists(t *testing.T) {
	f := &fakeAPI{versions: []string{"v1.14.0"}}
	c := NewCached(f, time.Millisecond)
	ctx := context.Background()
	if _, err := c.Versions(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := c.Versions(ctx); err != nil {
		t.Fatal(err)
	}
	if f.calls["versions"] != 2 {
		t.Fatalf("expected a refresh after the TTL, got %d calls", f.calls["versions"])
	}
}
