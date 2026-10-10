package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type sitePublishJob struct {
	If          string            `yaml:"if"`
	Needs       string            `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Environment struct {
		Name string `yaml:"name"`
	} `yaml:"environment"`
	Steps []map[string]any `yaml:"steps"`
}

func TestManualSitePublishWorkflowIsExactCommitAndLeastPrivilege(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "manual-site-publish.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]any            `yaml:"on"`
		Permissions map[string]any            `yaml:"permissions"`
		Jobs        map[string]sitePublishJob `yaml:"jobs"`
	}
	if err = yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse site publication workflow: %v", err)
	}
	if len(workflow.On) != 1 {
		t.Fatalf("site publisher must have only workflow_dispatch, got %#v", workflow.On)
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Fatal("site publisher is not manual-only")
	}
	if len(workflow.Permissions) != 0 {
		t.Fatalf("workflow-level permissions must be empty, got %#v", workflow.Permissions)
	}
	build, ok := workflow.Jobs["build"]
	if !ok || build.If != "github.ref == 'refs/heads/main'" {
		t.Fatalf("build must require a main-branch dispatch, got %#v", build)
	}
	if len(build.Permissions) != 1 || build.Permissions["contents"] != "read" {
		t.Fatalf("build permissions are not read-only: %#v", build.Permissions)
	}
	deploy, ok := workflow.Jobs["deploy"]
	if !ok || deploy.If != build.If || deploy.Needs != "build" || deploy.Environment.Name != "github-pages" {
		t.Fatalf("deploy must follow main-only build under github-pages environment, got %#v", deploy)
	}
	if len(deploy.Permissions) != 2 || deploy.Permissions["pages"] != "write" || deploy.Permissions["id-token"] != "write" {
		t.Fatalf("deploy permissions exceed Pages and OIDC needs: %#v", deploy.Permissions)
	}

	var sawCheckout, sawGoSetup, sawBuild, sawUpload, sawDeploy bool
	for _, step := range build.Steps {
		uses, _ := step["uses"].(string)
		if strings.HasPrefix(uses, "actions/") && !regexp.MustCompile(`^actions/[a-z-]+@[0-9a-f]{40}$`).MatchString(uses) {
			t.Errorf("action is not pinned to a full commit SHA: %s", uses)
		}
		with, _ := step["with"].(map[string]any)
		switch {
		case strings.HasPrefix(uses, "actions/checkout@"):
			sawCheckout = true
			if with["ref"] != "${{ github.sha }}" || with["persist-credentials"] != false {
				t.Errorf("checkout must use the exact dispatch commit without persisted credentials: %#v", with)
			}
		case strings.HasPrefix(uses, "actions/setup-go@"):
			sawGoSetup = true
			if with["go-version-file"] != "go.mod" {
				t.Errorf("Go setup must follow go.mod: %#v", with)
			}
		case strings.HasPrefix(uses, "actions/upload-pages-artifact@"):
			sawUpload = true
			if with["path"] != "site" {
				t.Errorf("Pages artifact must contain the generated site directory: %#v", with)
			}
		}
		if step["run"] == "go run ./cmd/factory --build-indexed-site --data-root data --site-root site" {
			sawBuild = true
		}
	}
	if !sawCheckout || !sawGoSetup || !sawBuild || !sawUpload {
		t.Fatalf("build is missing exact checkout, Go setup, catalog build, or artifact upload: %#v", build.Steps)
	}
	for _, step := range deploy.Steps {
		uses, _ := step["uses"].(string)
		if strings.HasPrefix(uses, "actions/") && !regexp.MustCompile(`^actions/[a-z-]+@[0-9a-f]{40}$`).MatchString(uses) {
			t.Errorf("action is not pinned to a full commit SHA: %s", uses)
		}
		if strings.HasPrefix(uses, "actions/deploy-pages@") {
			sawDeploy = true
		}
	}
	if !sawDeploy {
		t.Fatal("deploy job does not use the official Pages deployment action")
	}
	for _, forbidden := range []string{"factory-state", "--process-work", "--dispatch", "--accept", "--provider-collect"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("site publication workflow includes unrelated factory operation %q", forbidden)
		}
	}
}
