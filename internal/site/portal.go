package site

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

const indexedPortalTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} | AVM Intelligence</title>
<style>
:root{color-scheme:light;--ink:#1d302d;--muted:#536661;--paper:#f3f6f2;--white:#fff;--line:#cbd7d1;--green:#176b58;--rust:#b35339;--gold:#986b18}
*{box-sizing:border-box}body{margin:0;background:var(--paper);color:var(--ink);font:16px/1.5 "Avenir Next","Segoe UI",sans-serif}
header{display:flex;align-items:center;justify-content:space-between;gap:1rem;padding:1.1rem max(1.25rem,calc((100vw - 1200px)/2));background:var(--white);border-bottom:1px solid var(--line)}
.brand{font-family:Georgia,"Times New Roman",serif;font-size:1.35rem;color:var(--ink);text-decoration:none}.asof{font-size:.9rem;color:var(--muted)}
nav{display:flex;gap:.25rem;overflow-x:auto;padding:.6rem max(1rem,calc((100vw - 1200px)/2));background:#e4ece7;border-bottom:1px solid var(--line)}
nav a{white-space:nowrap;padding:.45rem .7rem;color:var(--ink);text-decoration:none;border-bottom:2px solid transparent}nav a[aria-current=page]{color:var(--green);border-color:var(--green);font-weight:650}
main{max-width:1200px;margin:0 auto;padding:1.5rem 1rem 3rem}h1,h2{font-family:Georgia,"Times New Roman",serif;font-weight:500;letter-spacing:0}h1{font-size:2rem;margin:.25rem 0 .35rem}h2{font-size:1.25rem;margin:1.6rem 0 .4rem}.meta{margin:0 0 1.2rem;color:var(--muted)}
.metrics{display:flex;flex-wrap:wrap;gap:0;margin:1rem 0 1.5rem;border-block:1px solid var(--line);background:var(--white)}.metric{min-width:130px;padding:.75rem 1rem;border-right:1px solid var(--line)}.metric dt{font-size:.78rem;color:var(--muted)}.metric dd{margin:.1rem 0 0;font-size:1.15rem;font-weight:650}
.section{margin-top:1.7rem}.table-wrap{overflow-x:auto;background:var(--white);border-block:1px solid var(--line)}table{width:100%;border-collapse:collapse;min-width:650px}caption{text-align:left;padding:.65rem .75rem;font-weight:650}th,td{text-align:left;vertical-align:top;padding:.6rem .75rem;border-top:1px solid #dce5e0}th{font-size:.82rem;color:var(--muted);font-weight:650}td{overflow-wrap:anywhere}a{color:var(--green)}code{font-size:.9em}.empty{padding:.8rem 1rem;border-left:3px solid var(--gold);background:#fff;color:var(--muted)}
label.filter{display:flex;align-items:center;gap:.5rem;margin:.75rem 0;color:var(--muted)}input[type=search]{max-width:24rem;width:100%;padding:.5rem .65rem;border:1px solid #8a9b95;background:#fff;color:var(--ink);font:inherit}
footer{max-width:1200px;margin:0 auto;padding:1rem;color:var(--muted);border-top:1px solid var(--line);font-size:.85rem}
.meta code,.metric dd,h1{overflow-wrap:anywhere}.metric{min-width:0;max-width:100%}label.filter input{min-width:0}
@media(max-width:640px){header{align-items:flex-start;flex-direction:column}.metric{flex:1 1 40%}main{padding-top:1rem}h1{font-size:1.65rem}}
</style>
</head>
<body>
<header><a class="brand" href="{{.RootPrefix}}index.html">AVM Intelligence</a><div class="asof">Catalog generation {{.Generation}} · as of {{.AsOf}}</div></header>
<nav aria-label="Primary">{{range .Navigation}}<a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav>
<main>
<h1>{{.Title}}</h1>
<p class="meta">Accepted dataset <code>{{.DatasetID}}</code></p>
<p class="meta">{{.PresentationStatus}}</p>
{{if .Metrics}}<dl class="metrics">{{range .Metrics}}<div class="metric"><dt>{{.Label}}</dt><dd>{{.Value}}</dd></div>{{end}}</dl>{{end}}
{{range .Sections}}<section class="section"><h2>{{.Title}}</h2>
{{if $.Filterable}}<label class="filter">Filter entries <input type="search" data-table-filter placeholder="Search entries"></label>{{end}}
{{if .Rows}}<div class="table-wrap"><table><caption>{{.Caption}}</caption><thead><tr><th>{{.PrimaryHeading}}</th>{{range .Columns}}<th>{{.}}</th>{{end}}</tr></thead><tbody>
{{range .Rows}}<tr data-search="{{.SearchText}}"><td>{{if .Href}}<a href="{{.Href}}">{{.Label}}</a>{{else}}{{.Label}}{{end}}</td>{{range .Values}}<td>{{.}}</td>{{end}}</tr>{{end}}
</tbody></table></div>{{else}}<p class="empty">{{.EmptyMessage}}</p>{{end}}
</section>{{end}}
</main>
<footer>Evidence is shown as accepted; missing, partial, and unchecked inputs are not passes. This page does not certify its own deployment.</footer>
{{if .Filterable}}<script>
document.querySelectorAll("[data-table-filter]").forEach(function(input){input.addEventListener("input",function(){var query=input.value.toLowerCase();document.querySelectorAll("tbody tr[data-search]").forEach(function(row){row.hidden=!row.dataset.search.toLowerCase().includes(query);});});});
</script>{{end}}
</body>
</html>`

type indexedPortalPage struct {
	Title              string
	DatasetID          string
	Generation         uint64
	AsOf               string
	RootPrefix         string
	Navigation         []portalNavigationLink
	PresentationStatus string
	Metrics            []portalMetric
	Sections           []portalSection
	Filterable         bool
}

type portalNavigationLink struct {
	Label   string
	Href    string
	Current bool
}

type portalMetric struct {
	Label string
	Value string
}

type portalSection struct {
	Title          string
	Caption        string
	PrimaryHeading string
	Columns        []string
	Rows           []portalTableRow
	EmptyMessage   string
}

type portalTableRow struct {
	Label      string
	Href       string
	Values     []string
	SearchText string
}

type presentationCoverageView struct {
	Capabilities map[string]string `json:"capabilities"`
	Complete     bool              `json:"complete"`
	ObservedAt   string            `json:"observed_at"`
	Reason       string            `json:"reason,omitempty"`
}

// BuildIndexed renders accepted native records. When no presentation snapshot
// has been accepted yet, it derives a temporary catalog-first view from the
// accepted dataset without writing presentation records or history.
func BuildIndexed(dataRoot, outputRoot string) error {
	return BuildIndexedAt(dataRoot, outputRoot, time.Now().UTC())
}

// BuildIndexedAt renders accepted source records using the caller's stable
// publication time. The dataset manifest is a derived index, so rebuilding a
// site does not depend on an older checked-in copy of that generated file.
func BuildIndexedAt(dataRoot, outputRoot string, asOf time.Time) error {
	if asOf.IsZero() {
		return fmt.Errorf("indexed portal build time is required")
	}
	records, err := index.ReadRecords(dataRoot, asOf)
	if err != nil {
		return fmt.Errorf("read accepted indexed data: %w", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return fmt.Errorf("validate accepted indexed data: %w", err)
	}
	var snapshot publish.PresentationSnapshot
	if indexes.Presentation.CurrentID == nil {
		if indexes.Dataset.CreatedAt.IsZero() {
			return fmt.Errorf("accepted dataset creation time is required to derive the catalog view")
		}
		modules, buildErr := buildPresentationModules(records, indexes, indexes.Dataset.CreatedAt)
		if buildErr != nil {
			return fmt.Errorf("derive catalog-first presentation: %w", buildErr)
		}
		snapshot = publish.PresentationSnapshot{
			AsOf: indexes.Dataset.CreatedAt.UTC(), Modules: modules,
			CollectorHealth: []publish.PresentationCollectorHealth{},
		}
	} else {
		snapshotPath, pathErr := identity.PresentationPath(*indexes.Presentation.CurrentID)
		if pathErr != nil {
			return pathErr
		}
		snapshotData, readErr := readAcceptedDataFile(dataRoot, snapshotPath)
		if readErr != nil {
			return readErr
		}
		if err = decodePortalJSON(snapshotData, &snapshot); err != nil {
			return fmt.Errorf("decode accepted presentation snapshot: %w", err)
		}
		if err = validateIndexedPresentation(records, indexes, snapshot); err != nil {
			return err
		}
	}
	providerSnapshots, err := loadAcceptedProviderSnapshots(dataRoot, records.ProviderSnapshots)
	if err != nil {
		return err
	}
	return renderIndexedPortal(outputRoot, records, indexes, snapshot, providerSnapshots)
}

func validateIndexedPresentation(records index.Records, indexes index.Indexes, snapshot publish.PresentationSnapshot) error {
	if snapshot.SchemaVersion != 2 || snapshot.RecordType != "presentation_snapshot" || snapshot.ID == "" ||
		indexes.Presentation.CurrentID == nil || snapshot.ID != *indexes.Presentation.CurrentID || len(snapshot.Modules) != len(records.Modules) {
		return fmt.Errorf("accepted presentation snapshot does not match the indexed catalog")
	}
	modules := append([]index.ModuleRecord(nil), records.Modules...)
	sort.Slice(modules, func(left, right int) bool { return modules[left].ModuleID < modules[right].ModuleID })
	for position, module := range modules {
		presentationModule := snapshot.Modules[position]
		if presentationModule.ModuleID != module.ModuleID || presentationModule.Lifecycle != module.Lifecycle {
			return fmt.Errorf("presentation membership differs from accepted catalog at %s", module.ModuleID)
		}
		if !json.Valid(presentationModule.Coverage) {
			return fmt.Errorf("presentation coverage is invalid for %s", module.ModuleID)
		}
	}
	return nil
}

func loadAcceptedProviderSnapshots(dataRoot string, records []index.ProviderSnapshotRecord) ([]assessment.ProviderSnapshot, error) {
	snapshots := make([]assessment.ProviderSnapshot, 0, len(records))
	for _, record := range records {
		path, err := identity.ProviderSnapshotManifestPath(record.Address, record.Version, record.ID)
		if err != nil {
			return nil, err
		}
		data, err := readAcceptedDataFile(dataRoot, path)
		if err != nil {
			return nil, err
		}
		var snapshot assessment.ProviderSnapshot
		if err = decodePortalJSON(data, &snapshot); err != nil {
			return nil, fmt.Errorf("decode accepted provider snapshot %s: %w", record.ID, err)
		}
		if snapshot.SchemaVersion != 2 || snapshot.RecordType != "provider_snapshot" || snapshot.SnapshotID != record.ID || snapshot.ProviderAddress != record.Address || snapshot.ProviderVersion != record.Version {
			return nil, fmt.Errorf("provider snapshot manifest differs from accepted provider index")
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(left, right int) bool {
		if snapshots[left].ProviderAddress != snapshots[right].ProviderAddress {
			return snapshots[left].ProviderAddress < snapshots[right].ProviderAddress
		}
		return snapshots[left].ProviderVersion < snapshots[right].ProviderVersion
	})
	return snapshots, nil
}

func readAcceptedDataFile(dataRoot, canonicalPath string) ([]byte, error) {
	if !strings.HasPrefix(canonicalPath, "data/") {
		return nil, fmt.Errorf("accepted portal path must be beneath data/")
	}
	relative := strings.TrimPrefix(canonicalPath, "data/")
	if err := identity.ValidateRelativePath(relative, false); err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(dataRoot)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("accepted portal data root must be a real directory")
	}
	current := dataRoot
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for position, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("accepted portal file path contains a symlink")
		}
		if position < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("accepted portal file parent is not a directory")
		}
		if position == len(parts)-1 && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("accepted portal payload is not a regular file")
		}
	}
	return os.ReadFile(current)
}

func decodePortalJSON(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON input must contain one value")
	}
	return nil
}

func renderIndexedPortal(outputRoot string, records index.Records, indexes index.Indexes, snapshot publish.PresentationSnapshot, providerSnapshots []assessment.ProviderSnapshot) error {
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		return err
	}
	modulesByID := make(map[string]publish.PresentationModule, len(snapshot.Modules))
	for _, module := range snapshot.Modules {
		modulesByID[module.ModuleID] = module
	}
	moduleIndexes := make(map[string]index.ModuleIndex, len(indexes.Modules))
	for _, module := range indexes.Modules {
		moduleIndexes[module.ModuleID] = module
	}
	base := indexedPortalPage{
		DatasetID: indexes.Dataset.DatasetID, Generation: indexes.CatalogGeneration,
		AsOf: snapshot.AsOf.UTC().Format(time.RFC3339), RootPrefix: "",
	}
	if snapshot.ID == "" {
		base.PresentationStatus = "Catalog-first view derived from accepted records; no accepted presentation snapshot exists."
	} else {
		base.PresentationStatus = "Accepted presentation snapshot " + snapshot.ID
	}
	moduleCount := len(snapshot.Modules)
	selectedCount := 0
	for _, module := range snapshot.Modules {
		selectedCount += len(module.SelectedReceiptIDs)
	}

	catalogRows := make([]portalTableRow, 0, moduleCount)
	for _, module := range records.Modules {
		presentation := modulesByID[module.ModuleID]
		moduleIndex := moduleIndexes[module.ModuleID]
		sourceSHA := presentation.DesiredSourceSHA
		factsCount := countModuleFacts(records.Facts, module.ModuleID)
		catalogRows = append(catalogRows, portalTableRow{
			Label: module.ModuleID, Href: "modules/" + url.PathEscape(module.ModuleID) + ".html",
			Values:     []string{module.Repository, module.Lifecycle, sourceSHA, fmt.Sprint(factsCount), fmt.Sprint(len(moduleIndex.CurrentSelections))},
			SearchText: strings.ToLower(module.ModuleID + " " + module.Repository + " " + module.Lifecycle),
		})
	}
	base.Title = "Catalog"
	base.Filterable = true
	base.Metrics = []portalMetric{{Label: "Modules", Value: fmt.Sprint(moduleCount)}, {Label: "Selections", Value: fmt.Sprint(selectedCount)}}
	base.Sections = []portalSection{{Title: "Modules", Caption: "Accepted catalog membership", PrimaryHeading: "Module", Columns: []string{"Repository", "Lifecycle", "Desired source", "Facts", "Selected contexts"}, Rows: catalogRows, EmptyMessage: "No catalog modules are present in this accepted dataset."}}
	if err := renderPortalPage(outputRoot, "index.html", base, "catalog"); err != nil {
		return err
	}

	qualityRows := make([]portalTableRow, 0)
	for _, dimension := range records.DimensionResults {
		for _, check := range dimension.Checks {
			qualityRows = append(qualityRows, portalTableRow{
				Label: dimension.ModuleID, Href: "modules/" + url.PathEscape(dimension.ModuleID) + ".html",
				Values:     []string{dimension.UnitPath, dimension.DefinitionID, check.ID, check.Status, check.Execution, check.Severity, check.Message},
				SearchText: strings.ToLower(dimension.ModuleID + " " + dimension.UnitPath + " " + check.ID + " " + check.Status),
			})
		}
	}
	page := base
	page.Title = "Quality"
	page.Metrics = []portalMetric{{Label: "Dimension results", Value: fmt.Sprint(len(records.DimensionResults))}, {Label: "Checks", Value: fmt.Sprint(len(qualityRows))}}
	page.Sections = []portalSection{{Title: "Check evidence", Caption: "Accepted dimension results", PrimaryHeading: "Module", Columns: []string{"Unit", "Definition", "Check", "Status", "Execution", "Severity", "Evidence message"}, Rows: qualityRows, EmptyMessage: "No accepted native assessment results are available."}}
	if err := renderPortalPage(outputRoot, "quality.html", page, "quality"); err != nil {
		return err
	}

	providerRows := buildProviderPortalRows(records, providerSnapshots)
	page = base
	page.Title = "Provider / Type"
	page.Metrics = []portalMetric{{Label: "Accepted snapshots", Value: fmt.Sprint(len(providerSnapshots))}, {Label: "Observed type usages", Value: fmt.Sprint(countProviderUsages(records.Facts))}}
	page.Sections = []portalSection{{Title: "Provider evidence", Caption: "Accepted snapshots and observed usage", PrimaryHeading: "Provider", Columns: []string{"Version", "Kind", "Type", "Coverage / module"}, Rows: providerRows, EmptyMessage: "No accepted provider snapshots or observed provider type usages are available."}}
	if err := renderPortalPage(outputRoot, "provider.html", page, "provider"); err != nil {
		return err
	}

	currencyRows := buildCurrencyPortalRows(records)
	page = base
	page.Title = "Currency"
	page.Metrics = []portalMetric{{Label: "Version targets", Value: fmt.Sprint(len(currencyRows))}}
	page.Sections = []portalSection{{Title: "Provider targets", Caption: "Accepted analysis target vectors and constraints", PrimaryHeading: "Module", Columns: []string{"Context", "Provider", "Target version", "Constraint", "Check status"}, Rows: currencyRows, EmptyMessage: "No accepted analysis target vectors or provider constraints are available."}}
	if err := renderPortalPage(outputRoot, "currency.html", page, "currency"); err != nil {
		return err
	}

	page = base
	page.Title = "Issues"
	page.Metrics = nil
	page.Sections = []portalSection{{Title: "Public observations", Caption: "Accepted sanitized module/provider observations", PrimaryHeading: "Origin", Columns: []string{"Observation", "Observed", "Coverage"}, EmptyMessage: "No accepted sanitized issue observations are available."}}
	if err := renderPortalPage(outputRoot, "issues.html", page, "issues"); err != nil {
		return err
	}

	activityRows := buildActivityPortalRows(records)
	page = base
	page.Title = "Activity"
	page.Metrics = []portalMetric{{Label: "Recorded events", Value: fmt.Sprint(len(activityRows))}}
	page.Sections = []portalSection{{Title: "Accepted activity", Caption: "Distinct source, assessment, and selection events", PrimaryHeading: "Event", Columns: []string{"Module", "Context / version", "Time", "Identity"}, Rows: activityRows, EmptyMessage: "No accepted version observations or selection receipts are available."}}
	if err := renderPortalPage(outputRoot, "activity.html", page, "activity"); err != nil {
		return err
	}

	operationsRows := make([]portalTableRow, 0, len(snapshot.CollectorHealth))
	for _, health := range snapshot.CollectorHealth {
		searchText := strings.ToLower(health.CollectorID + " " + health.Status + " " + health.ObservedAt.UTC().Format(time.RFC3339))
		operationsRows = append(operationsRows, portalTableRow{Label: health.CollectorID, Values: []string{health.Status, health.ObservedAt.UTC().Format(time.RFC3339), formatOptionalTime(health.LastGoodAt)}, SearchText: searchText})
	}
	page = base
	page.Title = "Operations"
	page.Metrics = []portalMetric{{Label: "Collector health records", Value: fmt.Sprint(len(operationsRows))}}
	page.Sections = []portalSection{{Title: "Collector health", Caption: "Sanitized accepted as-of status", PrimaryHeading: "Collector", Columns: []string{"Status", "Observed", "Last good"}, Rows: operationsRows, EmptyMessage: "No accepted collector health records are available."}}
	if err := renderPortalPage(outputRoot, "operations.html", page, "operations"); err != nil {
		return err
	}

	for _, module := range records.Modules {
		page = buildModulePortalPage(base, module, modulesByID[module.ModuleID], records, moduleIndexes[module.ModuleID])
		if err := renderPortalPage(outputRoot, filepath.ToSlash(filepath.Join("modules", module.ModuleID+".html")), page, ""); err != nil {
			return err
		}
	}
	return nil
}

func portalNavigation(prefix, current string) []portalNavigationLink {
	pages := []portalNavigationLink{
		{Label: "Catalog", Href: "index.html"}, {Label: "Quality", Href: "quality.html"},
		{Label: "Provider / Type", Href: "provider.html"}, {Label: "Currency", Href: "currency.html"},
		{Label: "Issues", Href: "issues.html"}, {Label: "Activity", Href: "activity.html"}, {Label: "Operations", Href: "operations.html"},
	}
	currentHref := current + ".html"
	if current == "catalog" {
		currentHref = pages[0].Href
	}
	for position := range pages {
		pages[position].Current = pages[position].Href == currentHref
		pages[position].Href = prefix + pages[position].Href
	}
	return pages
}

func renderPortalPage(outputRoot, relativePath string, page indexedPortalPage, current string) error {
	if err := identity.ValidateRelativePath(relativePath, false); err != nil {
		return err
	}
	page.Navigation = portalNavigation(page.RootPrefix, current)
	templateValue, err := template.New("indexed-portal").Parse(indexedPortalTemplate)
	if err != nil {
		return fmt.Errorf("parse indexed portal template: %w", err)
	}
	var output bytes.Buffer
	if err = templateValue.Execute(&output, page); err != nil {
		return fmt.Errorf("render indexed portal page %s: %w", relativePath, err)
	}
	path := filepath.Join(outputRoot, filepath.FromSlash(relativePath))
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, output.Bytes(), 0o644)
}

func buildProviderPortalRows(records index.Records, snapshots []assessment.ProviderSnapshot) []portalTableRow {
	rows := make([]portalTableRow, 0)
	for _, snapshot := range snapshots {
		coverage := "partial"
		if snapshot.Coverage.Complete {
			coverage = "complete"
		}
		for _, entry := range snapshot.Entries {
			rows = append(rows, portalTableRow{
				Label:      snapshot.ProviderAddress,
				Values:     []string{snapshot.ProviderVersion, entry.Kind, entry.Name, coverage},
				SearchText: strings.ToLower(snapshot.ProviderAddress + " " + entry.Kind + " " + entry.Name),
			})
			for _, source := range entry.IntelligenceSources {
				rows = append(rows, portalTableRow{
					Label:      snapshot.ProviderAddress,
					Href:       source.URL,
					Values:     []string{snapshot.ProviderVersion, source.Kind, entry.Name, source.VersionBasis + ": " + source.Version},
					SearchText: strings.ToLower(snapshot.ProviderAddress + " " + source.Kind + " " + entry.Name + " " + source.Version),
				})
			}
		}
	}
	for _, fact := range records.Facts {
		for _, usage := range fact.ProviderTypeUsages {
			rows = append(rows, portalTableRow{
				Label:      usage.ProviderAddress,
				Values:     []string{"observed", usage.Kind, usage.Name, usage.ModuleID},
				SearchText: strings.ToLower(usage.ProviderAddress + " " + usage.Kind + " " + usage.Name + " " + usage.ModuleID),
			})
		}
	}
	sort.Slice(rows, func(left, right int) bool {
		if rows[left].Label != rows[right].Label {
			return rows[left].Label < rows[right].Label
		}
		return strings.Join(rows[left].Values, "\x00") < strings.Join(rows[right].Values, "\x00")
	})
	return rows
}

func buildCurrencyPortalRows(records index.Records) []portalTableRow {
	constraintsByFact := providerRequirementsByFact(records.Facts)
	checksByFacts := providerConstraintChecks(records.DimensionResults)
	rows, matchedRequirements := currencyRowsFromAnalyses(records.Analyses, constraintsByFact, checksByFacts)
	rows = append(rows, unmatchedCurrencyRequirementRows(records.Facts, matchedRequirements)...)
	sort.Slice(rows, func(left, right int) bool {
		if rows[left].Label != rows[right].Label {
			return rows[left].Label < rows[right].Label
		}
		return strings.Join(rows[left].Values, "\x00") < strings.Join(rows[right].Values, "\x00")
	})
	return rows
}

func providerRequirementsByFact(facts []index.FactRecord) map[string][]index.ProviderRequirement {
	constraintsByFact := make(map[string][]index.ProviderRequirement, len(facts))
	for _, fact := range facts {
		constraintsByFact[fact.ID] = fact.ProviderRequirements
	}
	return constraintsByFact
}

func providerConstraintChecks(dimensions []assessment.DimensionResult) map[string]string {
	checksByFacts := make(map[string]string)
	for _, dimension := range dimensions {
		for _, check := range dimension.Checks {
			if check.ID == assessment.CheckProviderVersionConstraint {
				checksByFacts[dimension.ModuleID+"\x00"+dimension.FactsID] = check.Status
			}
		}
	}
	return checksByFacts
}

func currencyRowsFromAnalyses(analyses []index.AnalysisRecord, requirementsByFact map[string][]index.ProviderRequirement, checksByFacts map[string]string) ([]portalTableRow, map[string]struct{}) {
	rows := make([]portalTableRow, 0)
	matchedRequirements := make(map[string]struct{})
	for _, analysis := range analyses {
		status := checksByFacts[analysis.ModuleID+"\x00"+analysis.FactsID]
		if status == "" {
			status = "unchecked"
		}
		analysisRows, matched := currencyRowsForAnalysis(analysis, requirementsByFact[analysis.FactsID], status)
		rows = append(rows, analysisRows...)
		for key := range matched {
			matchedRequirements[key] = struct{}{}
		}
	}
	return rows, matchedRequirements
}

func unmatchedCurrencyRequirementRows(facts []index.FactRecord, matched map[string]struct{}) []portalTableRow {
	rows := make([]portalTableRow, 0)
	for _, fact := range facts {
		for _, requirement := range fact.ProviderRequirements {
			if _, exists := matched[currencyRequirementKey(requirement)]; exists {
				continue
			}
			rows = append(rows, currencyRow(requirement.ModuleID, "target unavailable", requirement.ProviderAddress, "unavailable", requirement.Constraint, "unchecked"))
		}
	}
	return rows
}

func currencyRowsForAnalysis(analysis index.AnalysisRecord, requirements []index.ProviderRequirement, status string) ([]portalTableRow, map[string]struct{}) {
	rows := make([]portalTableRow, 0)
	matched := make(map[string]struct{})
	for _, target := range analysis.ProviderTargets {
		matchingRequirements := 0
		for _, requirement := range requirements {
			if requirement.ProviderAddress != target.Address {
				continue
			}
			matchingRequirements++
			matched[currencyRequirementKey(requirement)] = struct{}{}
			rows = append(rows, currencyRow(analysis.ModuleID, analysis.ContextID, target.Address, target.Version, requirement.Constraint, status))
		}
		if matchingRequirements == 0 {
			rows = append(rows, currencyRow(analysis.ModuleID, analysis.ContextID, target.Address, target.Version, "not declared", status))
		}
	}
	return rows, matched
}

func currencyRequirementKey(requirement index.ProviderRequirement) string {
	return requirement.ModuleID + "\x00" + requirement.FactsID + "\x00" + requirement.ProviderAddress + "\x00" + requirement.Constraint
}

func currencyRow(moduleID, contextID, address, target, constraint, status string) portalTableRow {
	return portalTableRow{
		Label:      moduleID,
		Values:     []string{contextID, address, target, constraint, status},
		SearchText: strings.ToLower(moduleID + " " + contextID + " " + address + " " + status),
	}
}

func buildActivityPortalRows(records index.Records) []portalTableRow {
	rows := make([]portalTableRow, 0, len(records.VersionObservations)+len(records.Selections))
	for _, observation := range records.VersionObservations {
		rows = append(rows, portalTableRow{
			Label: "Source observation", Values: []string{observation.ModuleID, observation.Version, observation.ObservedAt.UTC().Format(time.RFC3339), observation.ID},
			SearchText: strings.ToLower(observation.ModuleID + " " + observation.Version),
		})
	}
	for _, selection := range records.Selections {
		rows = append(rows, portalTableRow{
			Label: "Selection receipt", Values: []string{selection.ModuleID, selection.ContextID, selection.SelectedAt.UTC().Format(time.RFC3339), selection.ID},
			SearchText: strings.ToLower(selection.ModuleID + " " + selection.ContextID),
		})
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].Values[2] > rows[right].Values[2] })
	return rows
}

func buildModulePortalPage(base indexedPortalPage, module index.ModuleRecord, presentation publish.PresentationModule, records index.Records, moduleIndex index.ModuleIndex) indexedPortalPage {
	page := base
	page.Title = module.ModuleID
	page.RootPrefix = "../"
	page.Metrics = []portalMetric{
		{Label: "Lifecycle", Value: module.Lifecycle},
		{Label: "Desired source", Value: presentation.DesiredSourceSHA},
		{Label: "Current contexts", Value: fmt.Sprint(len(moduleIndex.CurrentSelections))},
	}
	var checks, usages, selections []portalTableRow
	for _, fact := range records.Facts {
		if fact.ModuleID != module.ModuleID {
			continue
		}
		for _, usage := range fact.ProviderTypeUsages {
			searchText := strings.ToLower(usage.ProviderAddress + " " + usage.Kind + " " + usage.Name + " " + fact.SourceSHA)
			usages = append(usages, portalTableRow{Label: usage.ProviderAddress, Values: []string{usage.Kind, usage.Name, fact.SourceSHA}, SearchText: searchText})
		}
	}
	for _, dimension := range records.DimensionResults {
		if dimension.ModuleID != module.ModuleID {
			continue
		}
		for _, check := range dimension.Checks {
			searchText := strings.ToLower(dimension.UnitPath + " " + dimension.DefinitionID + " " + check.ID + " " + check.Status + " " + check.Execution + " " + check.Message)
			checks = append(checks, portalTableRow{Label: dimension.UnitPath, Values: []string{dimension.DefinitionID, check.ID, check.Status, check.Execution, check.Message}, SearchText: searchText})
		}
	}
	for _, selection := range records.Selections {
		if selection.ModuleID == module.ModuleID {
			selectedAt := selection.SelectedAt.UTC().Format(time.RFC3339)
			searchText := strings.ToLower(selection.ContextID + " " + fmt.Sprint(selection.Sequence) + " " + selection.ID + " " + selectedAt)
			selections = append(selections, portalTableRow{Label: selection.ContextID, Values: []string{fmt.Sprint(selection.Sequence), selection.ID, selectedAt}, SearchText: searchText})
		}
	}
	page.Sections = []portalSection{
		{Title: "Provider usage", Caption: "Typed accepted facts", PrimaryHeading: "Provider", Columns: []string{"Kind", "Type", "Facts source"}, Rows: usages, EmptyMessage: "No accepted provider type usage is available."},
		{Title: "Quality", Caption: "Accepted dimension evidence", PrimaryHeading: "Unit", Columns: []string{"Definition", "Check", "Status", "Execution", "Evidence"}, Rows: checks, EmptyMessage: "No accepted assessment results are available."},
		{Title: "Selections", Caption: "Append-only selection receipts", PrimaryHeading: "Context", Columns: []string{"Sequence", "Receipt", "Selected at"}, Rows: selections, EmptyMessage: "No selection receipts are available."},
	}
	return page
}

func countModuleFacts(facts []index.FactRecord, moduleID string) int {
	count := 0
	for _, fact := range facts {
		if fact.ModuleID == moduleID {
			count++
		}
	}
	return count
}

func countProviderUsages(facts []index.FactRecord) int {
	count := 0
	for _, fact := range facts {
		count += len(fact.ProviderTypeUsages)
	}
	return count
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return "unavailable"
	}
	return value.UTC().Format(time.RFC3339)
}
