package source

import (
	"path/filepath"
	"testing"
)

func TestLoadReviewedPilot(t *testing.T) {
	t.Parallel()
	pilot, err := LoadPilot(filepath.Join("..", "..", "config", "pilot.yaml"))
	if err != nil {
		t.Fatalf("LoadPilot() error = %v", err)
	}
	if pilot.Module.Repository != PilotRepository || pilot.Module.Commit != PilotCommit {
		t.Fatalf("pilot = %#v", pilot.Module)
	}
	want := "https://github.com/Azure/terraform-azurerm-avm-res-resources-resourcegroup/archive/2c605230f1bcb5dc29a667f2a43258bfa9140c32.tar.gz"
	if pilot.ArchiveURL() != want {
		t.Fatalf("ArchiveURL() = %s", pilot.ArchiveURL())
	}
}

func TestPilotRejectsMovedPin(t *testing.T) {
	t.Parallel()
	pilot := Pilot{Version: 1}
	pilot.Module.ID = "avm-res-resources-resourcegroup"
	pilot.Module.Type = "res"
	pilot.Module.Domain = "resources"
	pilot.Module.Repository = PilotRepository
	pilot.Module.Version = PilotVersion
	pilot.Module.Commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := pilot.Validate(); err == nil {
		t.Fatal("changed commit was accepted")
	}
}
