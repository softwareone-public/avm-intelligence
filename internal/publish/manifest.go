package publish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const ManifestSchemaVersion = 2

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var moduleIDPattern = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Manifest binds a generated site to the exact accepted source commit and
// input tree digests used to create it.
type Manifest struct {
	SchemaVersion  int       `json:"schema_version"`
	AcceptedCommit string    `json:"accepted_commit"`
	DataDigest     string    `json:"data_digest"`
	SiteDigest     string    `json:"site_digest"`
	ModuleIDs      []string  `json:"module_ids"`
	ProducerRun    string    `json:"producer_run"`
	GeneratedAt    time.Time `json:"generated_at"`
}

// Create computes a publication manifest without changing either input tree.
func Create(dataRoot, siteRoot, acceptedCommit, producerRun string, generatedAt time.Time) (Manifest, error) {
	if !commitPattern.MatchString(acceptedCommit) {
		return Manifest{}, fmt.Errorf("accepted commit must be a lowercase Git SHA")
	}
	if strings.TrimSpace(producerRun) == "" || generatedAt.IsZero() {
		return Manifest{}, fmt.Errorf("producer run and generation time are required")
	}
	dataDigest, err := DigestTree(dataRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest data tree: %w", err)
	}
	candidate, err := LoadCandidate(dataRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("read published module membership: %w", err)
	}
	moduleIDs, err := candidateModuleIDs(dataRoot, candidate)
	if err != nil {
		return Manifest{}, err
	}
	// The manifest is written into siteRoot after this digest is computed; omit
	// that self-referential file from the content digest.
	siteDigest, err := digestTree(siteRoot, map[string]struct{}{"publication.json": {}})
	if err != nil {
		return Manifest{}, fmt.Errorf("digest site tree: %w", err)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, AcceptedCommit: acceptedCommit,
		DataDigest: dataDigest, SiteDigest: siteDigest, ModuleIDs: moduleIDs, ProducerRun: producerRun,
		GeneratedAt: generatedAt.UTC(),
	}
	return manifest, manifest.Validate()
}

// DigestTree hashes regular files in lexical relative-path order. Symlinks and
// non-regular files are rejected so publication cannot follow unexpected paths.
func DigestTree(root string) (string, error) {
	return digestTree(root, nil)
}

func digestTree(root string, excluded map[string]struct{}) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("publication root is not a directory")
	}
	digest := sha256.New()
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular file is not allowed: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, skip := excluded[relative]; skip {
			return nil
		}
		if _, err = io.WriteString(digest, relative); err != nil {
			return err
		}
		if _, err = digest.Write([]byte{0}); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = digest.Write([]byte{0})
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Validate checks the manifest independently of its writer.
func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != ManifestSchemaVersion || !commitPattern.MatchString(manifest.AcceptedCommit) || !digestPattern(manifest.DataDigest) || !digestPattern(manifest.SiteDigest) || strings.TrimSpace(manifest.ProducerRun) == "" || manifest.GeneratedAt.IsZero() || len(manifest.ModuleIDs) == 0 {
		return fmt.Errorf("publication manifest is invalid")
	}
	previousModuleID := ""
	for _, moduleID := range manifest.ModuleIDs {
		if !moduleIDPattern.MatchString(moduleID) || (previousModuleID != "" && moduleID <= previousModuleID) {
			return fmt.Errorf("publication manifest module membership is invalid")
		}
		previousModuleID = moduleID
	}
	return nil
}

// VerifyDeploymentReceipt binds a producer receipt to the triggering workflow
// run and the exact accepted dataset tree that will be projected.
func VerifyDeploymentReceipt(receiptPath, dataRoot, expectedProducerRun string) (Manifest, error) {
	manifest, err := Read(receiptPath)
	if err != nil {
		return Manifest{}, err
	}
	if strings.TrimSpace(expectedProducerRun) == "" || manifest.ProducerRun != expectedProducerRun {
		return Manifest{}, fmt.Errorf("publication receipt producer run does not match deployment run")
	}
	dataDigest, err := DigestTree(dataRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest accepted dataset: %w", err)
	}
	if dataDigest != manifest.DataDigest {
		return Manifest{}, fmt.Errorf("publication receipt data digest does not match accepted dataset")
	}
	candidate, err := LoadCandidate(dataRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("read accepted module membership: %w", err)
	}
	moduleIDs, err := candidateModuleIDs(dataRoot, candidate)
	if err != nil {
		return Manifest{}, err
	}
	if len(moduleIDs) != len(manifest.ModuleIDs) {
		return Manifest{}, fmt.Errorf("publication receipt module membership does not match accepted dataset")
	}
	for index, moduleID := range moduleIDs {
		if manifest.ModuleIDs[index] != moduleID {
			return Manifest{}, fmt.Errorf("publication receipt module membership does not match accepted dataset")
		}
	}
	return manifest, nil
}

func candidateModuleIDs(dataRoot string, candidate Candidate) ([]string, error) {
	moduleIDs := make([]string, 0, len(candidate.Modules))
	for _, module := range candidate.Modules {
		reportPath := filepath.Join(dataRoot, filepath.FromSlash(module.ReportPath))
		info, err := os.Lstat(reportPath)
		if err != nil {
			return nil, fmt.Errorf("stat accepted report for %s: %w", module.ModuleID, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("accepted report for %s is not a regular file", module.ModuleID)
		}
		moduleIDs = append(moduleIDs, module.ModuleID)
	}
	return moduleIDs, nil
}

func digestPattern(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// Write atomically persists a validated manifest.
func Write(path string, manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".publication-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

// Read loads one strictly encoded publication manifest.
func Read(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err = decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse publication manifest: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, errors.New("publication manifest must contain one JSON document")
	}
	if err = manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
