package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileSupportsTargetArchBuilds(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(content)
	for _, want := range []string{
		"ARG TARGETOS",
		"ARG TARGETARCH",
		"GOOS=$TARGETOS GOARCH=$TARGETARCH",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("expected Dockerfile to contain %q for multi-arch builds", want)
		}
	}
	if strings.Contains(dockerfile, "GOARCH=amd64") {
		t.Fatal("expected Dockerfile not to hardcode amd64 builds")
	}
}

func TestGitHubWorkflowPublishesMultiArchImage(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "docker-publish.yml"))
	if err != nil {
		t.Fatalf("read docker publish workflow: %v", err)
	}
	workflow := string(content)
	for _, want := range []string{
		"docker/setup-qemu-action",
		"docker/setup-buildx-action",
		"docker/login-action",
		"docker/metadata-action",
		"docker/build-push-action",
		"needs: verify",
		"linux/amd64,linux/arm64",
		"ghcr.io/${{ github.repository_owner }}/vmq-go",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("expected workflow to contain %q", want)
		}
	}
}

func TestGitHubWorkflowHasQualityGate(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	workflow := string(content)
	for _, want := range []string{
		"go test ./... -count=1",
		"docker build -t vmq-go-ci:local .",
		"YAML.load_file('.github/workflows/docker-publish.yml')",
		"YAML.load_file('docker-compose.ghcr.yml')",
		"pull_request:",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("expected CI workflow to contain %q", want)
		}
	}
}

func TestGitHubWorkflowCreatesReleaseFromVersionTag(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(content)
	for _, want := range []string{
		"tags:",
		"v*",
		"gh release create",
		"ghcr.io/cvinit/vmq-go",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("expected release workflow to contain %q", want)
		}
	}
}

func TestReleaseComposePullsPublishedImage(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.ghcr.yml"))
	if err != nil {
		t.Fatalf("read ghcr compose file: %v", err)
	}
	compose := string(content)
	if !strings.Contains(compose, "image: ${VMQ_IMAGE:-ghcr.io/cvinit/vmq-go:latest}") {
		t.Fatal("expected GHCR compose file to default to ghcr.io/cvinit/vmq-go:latest")
	}
	if strings.Contains(compose, "build:") {
		t.Fatal("expected GHCR compose file not to build locally")
	}
}

func TestComposeDatabaseURLUsesConfiguredPostgresPassword(t *testing.T) {
	for _, name := range []string{"docker-compose.yml", "docker-compose.ghcr.yml"} {
		content, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatalf("read compose file %s: %v", name, err)
		}
		compose := string(content)
		if strings.Contains(compose, "postgres://vmq:vmq@postgres") {
			t.Fatalf("expected %s not to default app DATABASE_URL to vmq/vmq credentials", name)
		}
		if strings.Contains(compose, "POSTGRES_PASSWORD:-vmq") {
			t.Fatalf("expected %s not to default postgres service password to vmq", name)
		}
		if !strings.Contains(compose, "POSTGRES_PASSWORD") || !strings.Contains(compose, "DATABASE_URL") {
			t.Fatalf("expected %s to derive DATABASE_URL from configured postgres credentials", name)
		}
		if !strings.Contains(compose, "${DATABASE_URL:-") {
			t.Fatalf("expected %s to preserve explicit DATABASE_URL overrides", name)
		}
	}

	content, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	envExample := string(content)
	if strings.Contains(envExample, "postgres://vmq:vmq@postgres") {
		t.Fatal("expected .env.example not to document vmq/vmq database credentials")
	}
}

func TestDefaultDatabaseURLDoesNotUseVMQPassword(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_USER", "vmq")
	t.Setenv("POSTGRES_PASSWORD", "replace-postgres-password")
	t.Setenv("POSTGRES_DB", "vmq")
	t.Setenv("POSTGRES_HOST", "postgres")
	t.Setenv("POSTGRES_PORT", "5432")

	cfg := LoadConfig()
	if strings.Contains(cfg.DatabaseURL, "postgres://vmq:vmq@postgres") {
		t.Fatalf("expected generated DatabaseURL not to use vmq/vmq credentials, got %q", cfg.DatabaseURL)
	}
	if !strings.Contains(cfg.DatabaseURL, "replace-postgres-password") {
		t.Fatalf("expected generated DatabaseURL to include configured POSTGRES_PASSWORD, got %q", cfg.DatabaseURL)
	}
}

func TestReleaseGuideDocumentsVersioningConventions(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "docs", "RELEASING.md"))
	if err != nil {
		t.Fatalf("read release guide: %v", err)
	}
	guide := string(content)
	for _, want := range []string{
		"master",
		"vX.Y.Z",
		"latest",
		"git tag v1.0.0",
		"ghcr.io/cvinit/vmq-go:v1.0.0",
	} {
		if !strings.Contains(guide, want) {
			t.Fatalf("expected release guide to contain %q", want)
		}
	}
}

func TestDebianDeployScriptUsesGhcrCompose(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "scripts", "deploy-ghcr.sh"))
	if err != nil {
		t.Fatalf("read deploy script: %v", err)
	}
	script := string(content)
	for _, want := range []string{
		"docker-compose.ghcr.yml",
		".env.example",
		"docker compose -f docker-compose.ghcr.yml pull",
		"docker compose -f docker-compose.ghcr.yml up -d",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected deploy script to contain %q", want)
		}
	}
}

func TestDebianUpdateScriptPullsAndRestartsGhcrCompose(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "scripts", "update-ghcr.sh"))
	if err != nil {
		t.Fatalf("read update script: %v", err)
	}
	script := string(content)
	for _, want := range []string{
		"docker-compose.ghcr.yml",
		"docker compose -f docker-compose.ghcr.yml pull",
		"docker compose -f docker-compose.ghcr.yml up -d",
		"docker compose -f docker-compose.ghcr.yml ps",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected update script to contain %q", want)
		}
	}
}
