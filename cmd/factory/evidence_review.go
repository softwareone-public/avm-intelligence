package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

// validateEvidenceReviewTree reconstructs the exact immutable Git candidate;
// no candidate code is checked out or executed.
func validateEvidenceReviewTree(ctx context.Context, repositoryRoot string, input github.EvidenceReview) error {
	if !lowerHexSHA(input.HeadSHA, 40) || !lowerHexSHA(input.BaseSHA, 40) {
		return fmt.Errorf("review commits must be exact lowercase SHAs")
	}
	if _, err := reviewGit(ctx, repositoryRoot, "merge-base", "--is-ancestor", input.BaseSHA, input.HeadSHA); err != nil {
		return fmt.Errorf("review head does not descend from the exact validated base")
	}
	changed, err := reviewGit(ctx, repositoryRoot, "diff", "--no-renames", "--name-only", "-z", input.BaseSHA, input.HeadSHA)
	if err != nil {
		return err
	}
	if len(changed) == 0 || changed[len(changed)-1] != 0 {
		return fmt.Errorf("review candidate has no valid changed paths")
	}
	paths := strings.Split(string(changed[:len(changed)-1]), "\x00")
	for _, path := range paths {
		if path != "proposal.json" && !strings.HasPrefix(path, "data/") {
			return fmt.Errorf("review candidate changes a non-data path")
		}
		if err := identity.ValidateRelativePath(path, false); err != nil {
			return fmt.Errorf("review candidate has an unsafe path")
		}
	}
	root, err := os.MkdirTemp("", "factory-review-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	base, head, proposal, output := filepath.Join(root, "base"), filepath.Join(root, "head"), filepath.Join(root, "proposal"), filepath.Join(root, "candidate")
	remaining := 64 * 1024 * 1024
	if err := extractReviewTreeWithBudget(ctx, repositoryRoot, input.BaseSHA, base, &remaining, "data"); err != nil {
		return err
	}
	if err := extractReviewTreeWithBudget(ctx, repositoryRoot, input.HeadSHA, head, &remaining, "data", "proposal.json"); err != nil {
		return err
	}
	envelope, err := os.ReadFile(filepath.Join(head, "proposal.json"))
	if err != nil {
		return err
	}
	var identified publish.Proposal
	if err := decodeStrictJSON(envelope, &identified); err != nil || identified.ProposalID != input.ProposalID || identified.Kind != publish.ProposalModuleEvidence || identified.ExpectedBase != input.BaseSHA {
		return fmt.Errorf("review envelope does not match exact delivery identity")
	}
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(head, filepath.FromSlash(path)))
		if os.IsNotExist(err) && path != "proposal.json" {
			continue
		}
		if err != nil {
			return err
		}
		destination := filepath.Join(proposal, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(destination, data, 0600); err != nil {
			return err
		}
	}
	if err := publish.ApplyProposalPatch(proposal, input.BaseSHA, filepath.Join(base, "data"), output, paths); err != nil {
		return fmt.Errorf("review candidate fails typed acceptance: %w", err)
	}
	return compareReviewData(output, filepath.Join(head, "data"))
}

func reviewGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	resolved, err := providerCollectionDirectory(root)
	if err != nil {
		return nil, fmt.Errorf("review repository must be a real directory")
	}
	if _, err := providerCollectionDirectory(filepath.Join(resolved, ".git")); err != nil {
		return nil, fmt.Errorf("review requires a local non-linked Git repository")
	}
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(name, "GIT_") && name != "GIT_PAGER" && name != "GIT_MERGE_AUTOEDIT" && name != "GIT_EDITOR" && name != "GIT_ASKPASS" {
			return nil, fmt.Errorf("review Git environment overrides are prohibited")
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(bounded, "git", append([]string{"-C", resolved, "-c", "core.hooksPath=/dev/null", "-c", "core.attributesFile=/dev/null"}, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + resolved, "GIT_NO_REPLACE_OBJECTS=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1"}
	pipe, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open review object stream failed")
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start review object read failed")
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, 16*1024*1024+1))
	if readErr != nil || len(data) > 16*1024*1024 {
		cancel()
		_ = command.Wait()
		return nil, fmt.Errorf("review object exceeds read bounds")
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("read immutable review Git objects failed")
	}
	return data, nil
}

func extractReviewTree(ctx context.Context, repositoryRoot, commit, destination string, paths ...string) error {
	remaining := 64 * 1024 * 1024
	return extractReviewTreeWithBudget(ctx, repositoryRoot, commit, destination, &remaining, paths...)
}

func extractReviewTreeWithBudget(ctx context.Context, repositoryRoot, commit, destination string, remaining *int, paths ...string) error {
	listing, err := reviewGit(ctx, repositoryRoot, append([]string{"ls-tree", "-r", "-z", commit, "--"}, paths...)...)
	if err != nil {
		return err
	}
	if len(listing) == 0 || listing[len(listing)-1] != 0 {
		return fmt.Errorf("review tree listing is missing or truncated")
	}
	for _, entry := range strings.Split(string(listing[:len(listing)-1]), "\x00") {
		metadata, path, found := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !found || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" || !lowerHexSHA(fields[2], 40) {
			return fmt.Errorf("review tree requires regular non-executable blobs")
		}
		if err := identity.ValidateRelativePath(path, false); err != nil || (path != "data" && path != "proposal.json" && !strings.HasPrefix(path, "data/")) {
			return fmt.Errorf("unsafe review tree path")
		}
		filename := filepath.Join(destination, filepath.FromSlash(path))
		sizeBytes, err := reviewGit(ctx, repositoryRoot, "cat-file", "-s", fields[2])
		if err != nil {
			return err
		}
		size, err := strconv.Atoi(strings.TrimSpace(string(sizeBytes)))
		if err != nil || size < 0 || size > 16*1024*1024 || size > *remaining {
			return fmt.Errorf("review tree exceeds byte limits")
		}
		*remaining -= size
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			return err
		}
		data, err := reviewGit(ctx, repositoryRoot, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		if len(data) != size {
			return fmt.Errorf("review blob size changed")
		}
		if err := os.WriteFile(filename, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func compareReviewData(candidate, accepted string) error {
	for _, pair := range [][2]string{{candidate, accepted}, {accepted, candidate}} {
		if err := filepath.WalkDir(pair[0], func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(pair[0], path)
			if err != nil {
				return err
			}
			left, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			right, err := os.ReadFile(filepath.Join(pair[1], relative))
			if err != nil || !bytes.Equal(left, right) {
				return fmt.Errorf("review candidate differs from complete typed acceptance")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func openEvidenceReview(repositoryRoot, inputPath string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	var input github.EvidenceReview
	if err := decodeStrictJSON(data, &input); err != nil {
		return fmt.Errorf("decode review delivery input failed")
	}
	if input.Owner != factoryOwner || input.Repository != factoryRepository {
		return fmt.Errorf("review delivery repository is not configured factory repository")
	}
	client := github.ReviewClient{Token: os.Getenv("FACTORY_REVIEW_TOKEN"), ValidatePayload: func(ctx context.Context, exact github.EvidenceReview) error {
		return validateEvidenceReviewTree(ctx, repositoryRoot, exact)
	}}
	number, url, err := client.EnsureEvidenceReview(context.Background(), input)
	if err != nil {
		return err
	}
	slog.Info("evidence review ready", "proposal_id", input.ProposalID, "pull_request", number, "url", url)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"number": number, "url": url, "proposal_id": input.ProposalID, "head_sha": input.HeadSHA, "base_sha": input.BaseSHA})
}
