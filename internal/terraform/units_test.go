package terraform

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverUnitsUsesReviewedLayoutAndStableOrdering(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "versions.tf", "terraform {}")
	writeFixture(t, root, "examples/default/main.tf", "terraform {}")
	writeFixture(t, root, "modules/network/main.tf", "terraform {}")
	writeFixture(t, root, "modules/network/examples/peering/main.tf", "terraform {}")
	if err := os.MkdirAll(filepath.Join(root, ".terraform"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, ".terraform/ignored.tf", "this is not Terraform")

	units, err := DiscoverUnits(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []UnitInput{
		{Path: ".", Role: RoleComponent, Files: []string{"versions.tf"}},
		{Path: "examples/default", Role: RoleExample, Owner: ".", Files: []string{"main.tf"}},
		{Path: "modules/network", Role: RoleChildComponent, Owner: "modules/network", Files: []string{"main.tf"}},
		{Path: "modules/network/examples/peering", Role: RoleExample, Owner: "modules/network", Files: []string{"main.tf"}},
	}
	if !reflect.DeepEqual(units, want) {
		t.Fatalf("discovered units = %#v, want %#v", units, want)
	}
}

func TestDiscoverUnitsRejectsUnclassifiedTerraformPaths(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "tools/fixture/main.tf", "terraform {}")
	if _, err := DiscoverUnits(root); err == nil {
		t.Fatal("unsupported Terraform directory was silently omitted")
	}
}
