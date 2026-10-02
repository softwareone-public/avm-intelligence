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

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
)

const (
	ManifestSchemaVersion        = 2
	IndexedManifestSchemaVersion = 3
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var moduleIDPattern = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Manifest binds a generated site to the exact accepted source commit and
// input tree digests used to create it.
type Manifest struct {
	SchemaVersion        int                      `json:"schema_version"`
	AcceptedCommit       string                   `json:"accepted_commit"`
	DataDigest           string                   `json:"data_digest"`
	SiteDigest           string                   `json:"site_digest"`
	ModuleIDs            []string                 `json:"module_ids"`
	ProducerRun          string                   `json:"producer_run"`
	GeneratedAt          time.Time                `json:"generated_at"`
	DatasetID            string                   `json:"dataset_id,omitempty"`
	Generator            index.Producer           `json:"generator,omitempty"`
	ModuleMembership     []index.ModuleMembership `json:"module_membership,omitempty"`
	DeploymentGeneration uint64                   `json:"deployment_generation,omitempty"`
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

// CreateIndexed binds a complete typed dataset and its explicit
// module/context selection pointers to a serial deployment generation.
func CreateIndexed(dataRoot, siteRoot, acceptedCommit, producerRun string, deploymentGeneration uint64, generatedAt time.Time) (Manifest, error) {
	if !commitPattern.MatchString(acceptedCommit) || strings.TrimSpace(producerRun) == "" || deploymentGeneration == 0 || generatedAt.IsZero() {
		return Manifest{}, fmt.Errorf("accepted commit, producer run, deployment generation, and timestamp are required")
	}
	records, err := index.ReadRecords(dataRoot, generatedAt)
	if err != nil {
		return Manifest{}, fmt.Errorf("read indexed accepted dataset: %w", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return Manifest{}, fmt.Errorf("build indexed accepted dataset: %w", err)
	}
	dataDigest, err := DigestTree(dataRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest indexed data tree: %w", err)
	}
	siteDigest, err := digestTree(siteRoot, map[string]struct{}{"publication.json": {}})
	if err != nil {
		return Manifest{}, fmt.Errorf("digest indexed site tree: %w", err)
	}
	moduleIDs := make([]string, 0, len(indexes.Catalog.Modules))
	for _, module := range indexes.Catalog.Modules {
		moduleIDs = append(moduleIDs, module.ModuleID)
	}
	manifest := Manifest{
		SchemaVersion: IndexedManifestSchemaVersion, AcceptedCommit: acceptedCommit,
		DataDigest: dataDigest, SiteDigest: siteDigest, ModuleIDs: moduleIDs,
		ProducerRun: producerRun, GeneratedAt: generatedAt.UTC(),
		DatasetID: indexes.Dataset.DatasetID, Generator: indexes.Dataset.Generator,
		ModuleMembership:     append([]index.ModuleMembership(nil), indexes.Dataset.ModuleMembership...),
		DeploymentGeneration: deploymentGeneration,
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
	if err := validateManifestBase(manifest); err != nil {
		return err
	}
	if manifest.SchemaVersion == IndexedManifestSchemaVersion {
		return validateIndexedManifest(manifest)
	}
	if manifest.DatasetID != "" || manifest.Generator.ID != "" || len(manifest.ModuleMembership) != 0 || manifest.DeploymentGeneration != 0 {
		return fmt.Errorf("legacy publication manifest contains indexed fields")
	}
	return nil
}

func validateManifestBase(manifest Manifest) error {
	if (manifest.SchemaVersion != ManifestSchemaVersion && manifest.SchemaVersion != IndexedManifestSchemaVersion) || !commitPattern.MatchString(manifest.AcceptedCommit) || !digestPattern(manifest.DataDigest) || !digestPattern(manifest.SiteDigest) || strings.TrimSpace(manifest.ProducerRun) == "" || manifest.GeneratedAt.IsZero() || len(manifest.ModuleIDs) == 0 {
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

func validateIndexedManifest(manifest Manifest) error {
	if identity.ValidateRecordID("dataset", manifest.DatasetID) != nil || manifest.Generator.ID == "" || manifest.Generator.Version == "" || manifest.Generator.ContractVersion < 1 || manifest.DeploymentGeneration == 0 {
		return fmt.Errorf("indexed publication identity is invalid")
	}
	previousMembership := ""
	for _, member := range manifest.ModuleMembership {
		if err := validateManifestMember(manifest.ModuleIDs, member, previousMembership); err != nil {
			return err
		}
		previousMembership = member.ModuleID + "\x00" + member.ContextID
	}
	return nil
}

func validateManifestMember(moduleIDs []string, member index.ModuleMembership, previous string) error {
	key := member.ModuleID + "\x00" + member.ContextID
	if !containsManifestModule(moduleIDs, member.ModuleID) || identity.ValidateRelativePath(member.ContextID, false) != nil || (previous != "" && key <= previous) || member.SelectionID == nil {
		return fmt.Errorf("indexed publication module/context membership is invalid")
	}
	if member.AnalysisID != nil && identity.ValidateRecordID("analysis", *member.AnalysisID) != nil {
		return fmt.Errorf("indexed publication analysis membership is invalid")
	}
	if identity.ValidateRecordID("selection", *member.SelectionID) != nil {
		return fmt.Errorf("indexed publication selection membership is invalid")
	}
	return nil
}

func containsManifestModule(moduleIDs []string, moduleID string) bool {
	for _, candidate := range moduleIDs {
		if candidate == moduleID {
			return true
		}
	}
	return false
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

// VerifyIndexedDeploymentReceipt binds an indexed deployment receipt to the
// exact accepted data tree, trusted producer run, and active generation.
func VerifyIndexedDeploymentReceipt(receiptPath, dataRoot, expectedProducerRun string, expectedGeneration uint64) (Manifest, error) {
	manifest, err := Read(receiptPath)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.SchemaVersion != IndexedManifestSchemaVersion || expectedProducerRun == "" || manifest.ProducerRun != expectedProducerRun || expectedGeneration == 0 || manifest.DeploymentGeneration != expectedGeneration {
		return Manifest{}, fmt.Errorf("indexed deployment receipt does not match the active producer run and generation")
	}
	dataDigest, err := DigestTree(dataRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("digest accepted indexed dataset: %w", err)
	}
	if dataDigest != manifest.DataDigest {
		return Manifest{}, fmt.Errorf("indexed deployment receipt data digest does not match accepted dataset")
	}
	records, err := index.ReadRecords(dataRoot, manifest.GeneratedAt)
	if err != nil {
		return Manifest{}, fmt.Errorf("read accepted indexed dataset: %w", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return Manifest{}, fmt.Errorf("rebuild accepted indexed dataset: %w", err)
	}
	moduleIDs := make([]string, 0, len(indexes.Catalog.Modules))
	for _, module := range indexes.Catalog.Modules {
		moduleIDs = append(moduleIDs, module.ModuleID)
	}
	if manifest.DatasetID != indexes.Dataset.DatasetID || manifest.Generator != indexes.Dataset.Generator || !sameModuleMembership(manifest.ModuleMembership, indexes.Dataset.ModuleMembership) || !sameManifestStrings(manifest.ModuleIDs, moduleIDs) {
		return Manifest{}, fmt.Errorf("indexed deployment receipt membership does not match rebuilt accepted dataset")
	}
	return manifest, nil
}

func sameModuleMembership(left, right []index.ModuleMembership) bool {
	if len(left) != len(right) {
		return false
	}
	for position := range left {
		if left[position].ModuleID != right[position].ModuleID || left[position].ContextID != right[position].ContextID || !sameManifestNullable(left[position].AnalysisID, right[position].AnalysisID) || !sameManifestNullable(left[position].SelectionID, right[position].SelectionID) {
			return false
		}
	}
	return true
}

func sameManifestNullable(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameManifestStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for position := range left {
		if left[position] != right[position] {
			return false
		}
	}
	return true
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
