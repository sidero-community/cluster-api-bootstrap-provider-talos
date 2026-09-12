package imagefactory

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newServer(t *testing.T, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/versions":
			_, _ = w.Write([]byte(`["v1.13.9","v1.14.0","v1.14.2","v1.14.1","v1.15.0-alpha.1"]`))
		case r.Method == http.MethodGet && r.URL.Path == "/version/v1.14.2/extensions/official":
			_, _ = w.Write([]byte(`[{"name":"siderolabs/nvme-cli","ref":"ghcr.io/siderolabs/nvme-cli:v2.11","digest":"sha256:aa","author":"Sidero Labs","description":"nvme"},{"name":"siderolabs/intel-ucode","ref":"ghcr.io/siderolabs/intel-ucode:20250812","digest":"sha256:bb"}]`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/version/") && strings.HasSuffix(r.URL.Path, "/extensions/official"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/schematics":
			if r.Header.Get("Content-Type") != "application/yaml" {
				http.Error(w, "content type", http.StatusUnsupportedMediaType)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "siderolabs/does-not-exist") {
				http.Error(w, `{"error":"unknown extension siderolabs/does-not-exist"}`, http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + testID + `","schematic":"` + strings.ReplaceAll(string(body), "\n", "\\n") + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSchematicMarshal(t *testing.T) {
	s := Schematic{
		Overlay: &Overlay{Name: "rpi_generic", Image: "ghcr.io/siderolabs/sbc-raspberrypi"},
		Customization: Customization{
			SystemExtensions: SystemExtensions{OfficialExtensions: []string{"siderolabs/amd-ucode", "siderolabs/nvme-cli"}},
			ExtraKernelArgs:  []string{"vga=791"},
			Bootloader:       "sd-boot",
		},
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := "overlay:\n    image: ghcr.io/siderolabs/sbc-raspberrypi\n    name: rpi_generic\ncustomization:\n    systemExtensions:\n        officialExtensions:\n            - siderolabs/amd-ucode\n            - siderolabs/nvme-cli\n    extraKernelArgs:\n        - vga=791\n    bootloader: sd-boot\n"
	if string(out) != want {
		t.Fatalf("Marshal() =\n%s\nwant\n%s", out, want)
	}
	empty, _ := Schematic{}.Marshal()
	if string(empty) != "customization: {}\n" {
		t.Fatalf("empty = %q", empty)
	}
}

func TestClientCalls(t *testing.T) {
	srv := newServer(t, nil)
	c, err := NewClient(srv.URL+"/", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	versions, err := c.Versions(ctx)
	if err != nil || len(versions) != 5 {
		t.Fatalf("Versions() = %v, %v", versions, err)
	}

	exts, err := c.OfficialExtensions(ctx, "v1.14.2")
	if err != nil || strings.Join(exts, ",") != "siderolabs/intel-ucode,siderolabs/nvme-cli" {
		t.Fatalf("OfficialExtensions() = %v, %v", exts, err)
	}
	if _, err := c.OfficialExtensions(ctx, "v9.9.9"); err == nil || !IsFactoryError(err) {
		t.Fatalf("unknown version must be a factory error, got %v", err)
	}

	id, err := c.CreateSchematic(ctx, Schematic{Customization: Customization{SystemExtensions: SystemExtensions{OfficialExtensions: []string{"siderolabs/nvme-cli"}}}})
	if err != nil || id != testID {
		t.Fatalf("CreateSchematic() = %q, %v", id, err)
	}
	_, err = c.CreateSchematic(ctx, Schematic{Customization: Customization{SystemExtensions: SystemExtensions{OfficialExtensions: []string{"siderolabs/does-not-exist"}}}})
	if err == nil || !strings.Contains(err.Error(), "unknown extension") || !IsFactoryError(err) {
		t.Fatalf("expected the factory message to surface, got %v", err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")
	if got := c.InstallerImage(testID, "v1.14.2"); got != host+"/metal-installer/"+testID+":v1.14.2" {
		t.Fatalf("InstallerImage() = %q", got)
	}
}

func TestNewClientRejectsBadURLs(t *testing.T) {
	for _, bad := range []string{"", "factory.talos.dev", "ftp://x", "http://"} {
		if _, err := NewClient(bad, nil); err == nil {
			t.Errorf("NewClient(%q) accepted", bad)
		}
	}
	if _, err := NewClient(DefaultURL, nil); err != nil {
		t.Fatal(err)
	}
}
