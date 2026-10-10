package identity

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var moduleIDPathPattern = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)

const generatedIndexFilename = "index.json"

// Reference identifies an immutable record dependency by both ID and kind.
type Reference struct {
	ID   string
	Kind string
}

// CanonicalDataPath returns a fixed top-level path in the reviewed data tree.
func CanonicalDataPath(name string) (string, error) {
	switch name {
	case "schema", "catalog", "dataset":
		return path.Join("data", name+".json"), nil
	default:
		return "", fmt.Errorf("unknown canonical data document %q", name)
	}
}

// ModuleMetadataPath returns the catalog metadata path for a full module ID.
func ModuleMetadataPath(moduleID string) (string, error) {
	moduleType, err := moduleTypeForID(moduleID)
	if err != nil {
		return "", err
	}
	return path.Join("data", "modules", moduleType, moduleID, "module.json"), nil
}

// ModuleVersionsPath returns the append-only alias and observation index path.
func ModuleVersionsPath(moduleID string) (string, error) {
	moduleType, err := moduleTypeForID(moduleID)
	if err != nil {
		return "", err
	}
	return path.Join("data", "modules", moduleType, moduleID, "versions.json"), nil
}

// ModuleIndexPath returns the generated summary index path for one module.
func ModuleIndexPath(moduleID string) (string, error) {
	metadataPath, err := ModuleMetadataPath(moduleID)
	if err != nil {
		return "", err
	}
	return path.Join(path.Dir(metadataPath), generatedIndexFilename), nil
}

// TrackIndexPath returns the generated index path for one explicit context ID.
func TrackIndexPath(contextID string) (string, error) {
	if err := ValidateRelativePath(contextID, false); err != nil {
		return "", fmt.Errorf("invalid track context ID: %w", err)
	}
	return path.Join("data", "tracks", contextID, generatedIndexFilename), nil
}

// ModuleVersionObservationPath returns one immutable release/tag/SHA observation.
func ModuleVersionObservationPath(moduleID, observationID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateRecordID("version-observation", observationID); err != nil {
		return "", err
	}
	return path.Join("data", "observations", "modules", moduleID, observationID+".json"), nil
}

// ModuleSourcePath returns the canonical immutable source path for a full
// module ID and source SHA.
func ModuleSourcePath(moduleID, sourceSHA string) (string, error) {
	moduleType, err := moduleTypeForID(moduleID)
	if err != nil {
		return "", err
	}
	if !sha1Pattern.MatchString(sourceSHA) {
		return "", fmt.Errorf("invalid source SHA %q", sourceSHA)
	}
	return path.Join("data", "modules", moduleType, moduleID, "revisions", sourceSHA, "source.json"), nil
}

// ModuleRecordPath returns the canonical path for a content-addressed record
// beneath one immutable source revision.
func ModuleRecordPath(moduleID, sourceSHA, recordKind, recordID string) (string, error) {
	sourcePath, err := ModuleSourcePath(moduleID, sourceSHA)
	if err != nil {
		return "", err
	}
	if err = ValidateRecordID(recordKind, recordID); err != nil {
		return "", err
	}
	base := path.Dir(sourcePath)
	directory, exists := map[string]string{
		"facts":            "facts",
		"provenance":       "provenance",
		"dimension-result": "dimensions",
		"analysis":         "analyses",
	}[recordKind]
	if !exists {
		return "", fmt.Errorf("record kind %q is not stored under a module revision", recordKind)
	}
	return path.Join(base, directory, recordID+".json"), nil
}

// DefinitionPath returns the canonical path for an immutable assessment definition.
func DefinitionPath(definitionID string) (string, error) {
	if err := ValidateRecordID("definition", definitionID); err != nil {
		return "", err
	}
	return path.Join("data", "assessment", "definitions", definitionID+".json"), nil
}

// SelectionPath returns the canonical path for a context-scoped selection receipt.
func SelectionPath(moduleID, contextID, selectionID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateRelativePath(contextID, false); err != nil {
		return "", fmt.Errorf("invalid context ID: %w", err)
	}
	if err := ValidateRecordID("selection", selectionID); err != nil {
		return "", err
	}
	return path.Join("data", "selections", moduleID, contextID, selectionID+".json"), nil
}

// PresentationPath returns the canonical path for an immutable presentation snapshot.
func PresentationPath(presentationID string) (string, error) {
	if err := ValidateRecordID("presentation", presentationID); err != nil {
		return "", err
	}
	return path.Join("data", "presentation", presentationID+".json"), nil
}

// ModuleObservationPath returns the canonical path for a sanitized module observation.
func ModuleObservationPath(moduleID, observationID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateRecordID("observation", observationID); err != nil {
		return "", err
	}
	return path.Join("data", "observations", "modules", moduleID, observationID+".json"), nil
}

// ProviderSnapshotPath returns a path under the full registry-host/namespace/provider
// identity so provider names from different registries cannot collide.
func ProviderSnapshotPath(providerAddress, version, snapshotID string) (string, error) {
	if err := ValidateRelativePath(providerAddress, false); err != nil {
		return "", fmt.Errorf("invalid provider address: %w", err)
	}
	if len(strings.Split(providerAddress, "/")) != 3 {
		return "", fmt.Errorf("provider address must include host, namespace, and name")
	}
	if err := ValidateRelativePath(version, false); err != nil {
		return "", fmt.Errorf("invalid provider version: %w", err)
	}
	if err := ValidateRecordID("snapshot", snapshotID); err != nil {
		return "", err
	}
	return path.Join("data", "providers", providerAddress, "versions", version, "snapshots", snapshotID), nil
}

// ProviderMetadataPath returns the metadata path using the full provider address.
func ProviderMetadataPath(providerAddress string) (string, error) {
	if err := validateProviderAddress(providerAddress); err != nil {
		return "", err
	}
	return path.Join("data", "providers", providerAddress, "provider.json"), nil
}

// ProviderIndexPath returns the generated index path using the full provider address.
func ProviderIndexPath(providerAddress string) (string, error) {
	metadataPath, err := ProviderMetadataPath(providerAddress)
	if err != nil {
		return "", err
	}
	return path.Join(path.Dir(metadataPath), generatedIndexFilename), nil
}

// DimensionDependencyIndexPath returns the generated assessment dependency index path.
func DimensionDependencyIndexPath() string {
	return path.Join("data", "assessment", "dependencies.json")
}

// PresentationIndexPath returns the generated presentation snapshot index path.
func PresentationIndexPath() string {
	return path.Join("data", "presentation", "index.json")
}

// ProviderSnapshotManifestPath returns the manifest path for one immutable snapshot.
func ProviderSnapshotManifestPath(providerAddress, version, snapshotID string) (string, error) {
	snapshotPath, err := ProviderSnapshotPath(providerAddress, version, snapshotID)
	if err != nil {
		return "", err
	}
	return path.Join(snapshotPath, "manifest.json"), nil
}

// ProviderTypeRecordPath returns one provider-qualified type record path.
func ProviderTypeRecordPath(providerAddress, version, snapshotID, kind, typeName string) (string, error) {
	snapshotPath, err := ProviderSnapshotPath(providerAddress, version, snapshotID)
	if err != nil {
		return "", err
	}
	directory, exists := map[string]string{
		"resource":   "resources",
		"datasource": "datasources",
		"ephemeral":  "ephemerals",
		"function":   "functions",
		"action":     "actions",
	}[kind]
	if !exists {
		return "", fmt.Errorf("unsupported provider type kind %q", kind)
	}
	if err = ValidateRelativePath(typeName, false); err != nil {
		return "", fmt.Errorf("invalid provider type name: %w", err)
	}
	return path.Join(snapshotPath, directory, typeName+".json"), nil
}

// ProviderKnowledgePath returns an immutable provider-knowledge record path.
func ProviderKnowledgePath(providerAddress, knowledgeID string) (string, error) {
	if err := validateProviderAddress(providerAddress); err != nil {
		return "", err
	}
	if err := ValidateRecordID("knowledge", knowledgeID); err != nil {
		return "", err
	}
	return path.Join("data", "providers", providerAddress, "knowledge", knowledgeID), nil
}

// CoreFunctionSnapshotPath returns the canonical Terraform-core function
// snapshot directory for one version and content-addressed snapshot.
func CoreFunctionSnapshotPath(terraformVersion, snapshotID string) (string, error) {
	if err := ValidateRelativePath(terraformVersion, false); err != nil {
		return "", fmt.Errorf("invalid Terraform version: %w", err)
	}
	if err := ValidateRecordID("snapshot", snapshotID); err != nil {
		return "", err
	}
	return path.Join("data", "terraform", "core", terraformVersion, "snapshots", snapshotID), nil
}

// CoreFunctionSnapshotFilePath returns the canonical core-function payload path.
func CoreFunctionSnapshotFilePath(terraformVersion, snapshotID string) (string, error) {
	snapshotPath, err := CoreFunctionSnapshotPath(terraformVersion, snapshotID)
	if err != nil {
		return "", err
	}
	return path.Join(snapshotPath, "functions.json"), nil
}

// StateDiscoveryPath returns the latest durable catalog observation path.
func StateDiscoveryPath() string {
	return "state/discovery/catalog.json"
}

// StateSourcePinPath returns the prior immutable source pin path for a module.
func StateSourcePinPath(moduleID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	return path.Join("state", "discovery", "source-pins", moduleID+".json"), nil
}

// StateDiscoveryWorkPath returns the module-owned durable discovery control
// record path.
func StateDiscoveryWorkPath(moduleID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	return path.Join("state", "discovery", "work", moduleID+".json"), nil
}

// StateModulePath returns the operational module lifecycle and work summary path.
func StateModulePath(moduleID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	return path.Join("state", "modules", moduleID+".json"), nil
}

// StateRequestPath returns the durable request record path.
func StateRequestPath(requestID string) (string, error) {
	if err := ValidateRecordID("request", requestID); err != nil {
		return "", err
	}
	return path.Join("state", "requests", requestID+".json"), nil
}

// StateAttemptPath returns the durable attempt record path for a request.
func StateAttemptPath(requestID, attemptID string) (string, error) {
	if err := ValidateRecordID("request", requestID); err != nil {
		return "", err
	}
	if err := ValidateRecordID("attempt", attemptID); err != nil {
		return "", err
	}
	return path.Join("state", "attempts", requestID, attemptID+".json"), nil
}

// StateSelectionPath returns the operational selection receipt path.
func StateSelectionPath(moduleID, contextID, selectionID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateRelativePath(contextID, false); err != nil {
		return "", fmt.Errorf("invalid context ID: %w", err)
	}
	if err := ValidateRecordID("selection", selectionID); err != nil {
		return "", err
	}
	return path.Join("state", "selections", moduleID, contextID, selectionID+".json"), nil
}

// StateExportPath returns the durable proposal-export outbox path.
func StateExportPath(proposalID string) (string, error) {
	if err := ValidateRecordID("proposal", proposalID); err != nil {
		return "", err
	}
	return path.Join("state", "exports", proposalID+".json"), nil
}

// StateDeploymentPath returns the operational deployment receipt path.
func StateDeploymentPath(deploymentID string) (string, error) {
	if err := ValidateRecordID("deployment", deploymentID); err != nil {
		return "", err
	}
	return path.Join("state", "publications", deploymentID+".json"), nil
}

// StateModulePublicationAcknowledgementPath returns one module-owned receipt
// for exact membership in a verified Pages deployment.
func StateModulePublicationAcknowledgementPath(moduleID, acknowledgementID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateRecordID("module-publication-ack", acknowledgementID); err != nil {
		return "", err
	}
	return path.Join("state", "publications", "modules", moduleID, acknowledgementID+".json"), nil
}

// StateOperationReceiptPath returns the immutable receipt path for one
// module-owned state operation.
func StateOperationReceiptPath(moduleID, operationID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateOperationalID(operationID); err != nil {
		return "", err
	}
	return path.Join("state", "operations", "receipts", moduleID, operationID+".json"), nil
}

// StateProjectionIntentPath returns one durable projection intent path.
func StateProjectionIntentPath(moduleID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	return path.Join("state", "projection", "intents", moduleID+".json"), nil
}

// StateLifecycleSlotPath returns the durable module-owned AVM release slot.
func StateLifecycleSlotPath(moduleID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	return path.Join("state", "lifecycle", "slots", moduleID+".json"), nil
}

// StateProjectionHandoffPath returns the immutable module-owned path for one stage handoff.
func StateProjectionHandoffPath(moduleID, handoffID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateRecordID("stage-handoff", handoffID); err != nil {
		return "", err
	}
	return path.Join("state", "projection", "handoffs", moduleID, handoffID+".json"), nil
}

// StateProjectionAcknowledgementPath returns the exact-tuple acknowledgement path.
func StateProjectionAcknowledgementPath(moduleID, projectionIntentID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateRecordID("projection-intent", projectionIntentID); err != nil {
		return "", err
	}
	return path.Join("state", "projection", "acknowledgements", moduleID, projectionIntentID+".json"), nil
}

// StateProjectionBarrierReleasePath returns a separate timeout-release result path.
func StateProjectionBarrierReleasePath(moduleID, projectionIntentID string) (string, error) {
	if _, err := moduleTypeForID(moduleID); err != nil {
		return "", err
	}
	if err := ValidateRecordID("projection-intent", projectionIntentID); err != nil {
		return "", err
	}
	return path.Join("state", "projection", "barrier-releases", moduleID, projectionIntentID+".json"), nil
}

// StateCollectorHealthPath returns one collector health record path.
func StateCollectorHealthPath(collectorID string) (string, error) {
	if err := ValidateRelativePath(collectorID, false); err != nil {
		return "", fmt.Errorf("invalid collector ID: %w", err)
	}
	return path.Join("state", "health", collectorID+".json"), nil
}

// ValidateRecordID checks that id is a SHA-256 identity with the expected kind prefix.
func ValidateRecordID(kind, id string) error {
	if !recordKindPattern.MatchString(kind) {
		return fmt.Errorf("invalid record kind %q", kind)
	}
	if len(id) != len(kind)+1+64 || !strings.HasPrefix(id, kind+"-") {
		return fmt.Errorf("record ID does not match kind %q", kind)
	}
	if !sha256Pattern.MatchString(strings.TrimPrefix(id, kind+"-")) {
		return fmt.Errorf("record ID digest is invalid")
	}
	return nil
}

// ValidateReferences rejects dangling references and references whose target
// kind does not match the record's declared expectation.
func ValidateReferences(available map[string]string, references []Reference) error {
	for _, reference := range references {
		if err := ValidateRecordID(reference.Kind, reference.ID); err != nil {
			return fmt.Errorf("invalid reference: %w", err)
		}
		actualKind, exists := available[reference.ID]
		if !exists {
			return fmt.Errorf("dangling %s reference %s", reference.Kind, reference.ID)
		}
		if actualKind != reference.Kind {
			return fmt.Errorf("reference %s has kind %s, expected %s", reference.ID, actualKind, reference.Kind)
		}
	}
	return nil
}

func validateProviderAddress(providerAddress string) error {
	if err := ValidateRelativePath(providerAddress, false); err != nil {
		return fmt.Errorf("invalid provider address: %w", err)
	}
	if len(strings.Split(providerAddress, "/")) != 3 {
		return fmt.Errorf("provider address must include host, namespace, and name")
	}
	return nil
}

func moduleTypeForID(moduleID string) (string, error) {
	if !moduleIDPathPattern.MatchString(moduleID) {
		return "", fmt.Errorf("invalid module ID %q", moduleID)
	}
	return strings.SplitN(moduleID, "-", 3)[1], nil
}
