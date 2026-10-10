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
	IndexedManifestSchemaVersion = 3
	indexedManifestFileName      = "publication.json"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var moduleIDPattern = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Manifest binds a generated site to the exact accepted source commit and
// input tree digests used to create it.
type Manifest struct {
	SchemaVersion        int                      `json:"schema_version"`
	AcceptedCommit       string                   `json:"accepted_commit"`
	DataDigest           string                   `json:"data_digest"`
	ModuleDataDigest     string                   `json:"module_data_digest"`
	ProviderDataDigest   string                   `json:"provider_data_digest"`
	SiteDigest           string                   `json:"site_digest"`
	ModuleIDs            []string                 `json:"module_ids"`
	ProducerRun          string                   `json:"producer_run"`
	GeneratedAt          time.Time                `json:"generated_at"`
	DatasetID            string                   `json:"dataset_id,omitempty"`
	Generator            index.Producer           `json:"generator,omitempty"`
	ModuleMembership     []index.ModuleMembership `json:"module_membership,omitempty"`
	DeploymentGeneration uint64                   `json:"deployment_generation,omitempty"`
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
	moduleDataDigest, err := DigestSubtree(dataRoot, "modules")
	if err != nil {
		return Manifest{}, fmt.Errorf("digest accepted module data root: %w", err)
	}
	providerDataDigest, err := DigestSubtree(dataRoot, "providers")
	if err != nil {
		return Manifest{}, fmt.Errorf("digest accepted provider data root: %w", err)
	}
	siteDigest, err := digestTree(siteRoot, map[string]struct{}{indexedManifestFileName: {}})
	if err != nil {
		return Manifest{}, fmt.Errorf("digest indexed site tree: %w", err)
	}
	moduleIDs := make([]string, 0, len(indexes.Catalog.Modules))
	for _, module := range indexes.Catalog.Modules {
		moduleIDs = append(moduleIDs, module.ModuleID)
	}
	manifest := Manifest{
		SchemaVersion: IndexedManifestSchemaVersion, AcceptedCommit: acceptedCommit,
		DataDigest: dataDigest, ModuleDataDigest: moduleDataDigest,
		ProviderDataDigest: providerDataDigest, SiteDigest: siteDigest, ModuleIDs: moduleIDs,
		ProducerRun: producerRun, GeneratedAt: generatedAt.UTC(),
		DatasetID: indexes.Dataset.DatasetID, Generator: indexes.Dataset.Generator,
		ModuleMembership:     append([]index.ModuleMembership(nil), indexes.Dataset.ModuleMembership...),
		DeploymentGeneration: deploymentGeneration,
	}
	return manifest, manifest.Validate()
}

// DigestSubtree hashes one independently accepted data root. A missing root
// has the digest of an empty tree, allowing the first module-only or
// provider-only publication to retain an explicit identity for both roots.
func DigestSubtree(dataRoot, relativeRoot string) (string, error) {
	dataInfo, err := os.Lstat(dataRoot)
	if err != nil {
		return "", err
	}
	if dataInfo.Mode()&os.ModeSymlink != 0 || !dataInfo.IsDir() {
		return "", fmt.Errorf("publication data root must be a real directory")
	}
	if relativeRoot != "modules" && relativeRoot != "providers" {
		return "", fmt.Errorf("publication subtree must be modules or providers")
	}
	root := filepath.Join(dataRoot, relativeRoot)
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return digestTreeContents(nil), nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("publication subtree must be a real directory")
	}
	return DigestTree(root)
}

func digestTreeContents(files []byte) string {
	digest := sha256.Sum256(files)
	return hex.EncodeToString(digest[:])
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
	return validateIndexedManifest(manifest)
}

func validateManifestBase(manifest Manifest) error {
	if manifest.SchemaVersion != IndexedManifestSchemaVersion || !commitPattern.MatchString(manifest.AcceptedCommit) || !digestPattern(manifest.DataDigest) || !digestPattern(manifest.ModuleDataDigest) || !digestPattern(manifest.ProviderDataDigest) || !digestPattern(manifest.SiteDigest) || strings.TrimSpace(manifest.ProducerRun) == "" || manifest.GeneratedAt.IsZero() || len(manifest.ModuleIDs) == 0 {
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
		previousMembership = member.ModuleID + "\x00" + member.AVMReleaseVersion + "\x00" + member.ContextID
	}
	return nil
}

func validateManifestMember(moduleIDs []string, member index.ModuleMembership, previous string) error {
	key := member.ModuleID + "\x00" + member.AVMReleaseVersion + "\x00" + member.ContextID
	if !containsManifestModule(moduleIDs, member.ModuleID) || !releaseVersionPattern.MatchString(member.AVMReleaseVersion) || identity.ValidateRelativePath(member.ContextID, false) != nil || (previous != "" && key <= previous) || member.SelectionID == nil {
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
	moduleDataDigest, err := DigestSubtree(dataRoot, "modules")
	if err != nil {
		return Manifest{}, fmt.Errorf("digest accepted module data root: %w", err)
	}
	providerDataDigest, err := DigestSubtree(dataRoot, "providers")
	if err != nil {
		return Manifest{}, fmt.Errorf("digest accepted provider data root: %w", err)
	}
	if moduleDataDigest != manifest.ModuleDataDigest || providerDataDigest != manifest.ProviderDataDigest {
		return Manifest{}, fmt.Errorf("indexed deployment receipt module or provider root digest does not match accepted data")
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

// VerifyIndexedPublication verifies the exact data inputs and rendered site
// tree named by one combined publication manifest.
func VerifyIndexedPublication(receiptPath, dataRoot, siteRoot, expectedProducerRun string, expectedGeneration uint64) (Manifest, error) {
	manifest, err := VerifyIndexedDeploymentReceipt(receiptPath, dataRoot, expectedProducerRun, expectedGeneration)
	if err != nil {
		return Manifest{}, err
	}
	siteDigest, err := digestTree(siteRoot, map[string]struct{}{indexedManifestFileName: {}})
	if err != nil {
		return Manifest{}, fmt.Errorf("digest rendered indexed site: %w", err)
	}
	if siteDigest != manifest.SiteDigest {
		return Manifest{}, fmt.Errorf("indexed publication receipt site digest does not match rendered site")
	}
	return manifest, nil
}

func sameModuleMembership(left, right []index.ModuleMembership) bool {
	if len(left) != len(right) {
		return false
	}
	for position := range left {
		if left[position].ModuleID != right[position].ModuleID || left[position].AVMReleaseVersion != right[position].AVMReleaseVersion || left[position].ContextID != right[position].ContextID || !sameManifestNullable(left[position].AnalysisID, right[position].AnalysisID) || !sameManifestNullable(left[position].SelectionID, right[position].SelectionID) {
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
