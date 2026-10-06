package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"sync"

	"github.com/softwareone-public/avm-intelligence/internal/progress"
)

var ErrProjectionSnapshotIncomplete = errors.New("projection Issue snapshot is incomplete")

var issueMarkerPattern = regexp.MustCompile(`<!-- factory-module-id:([a-z0-9-]+) -->`)

// ProjectionMetadataCache scopes stable Project metadata to one bounded run.
// It prevents Project identity and field-option reads from repeating per module.
type ProjectionMetadataCache struct {
	mu                 sync.Mutex
	projects           map[string]string
	fields             map[string]map[string]fieldRef
	issues             map[string]map[string][]Issue
	items              map[string]map[int]ProjectItemSnapshot
	checkpointSnapshot func(context.Context, string, string) error
	currentStateHead   string
	repoLabels         map[string]map[string]bool
	repoLabelKnown     map[string]map[string]bool
}

// ProjectionIssuePage is one resumable page from the exact-marker candidate snapshot.
type ProjectionIssuePage struct {
	Page       int
	NextPage   int
	TotalCount int
	Issues     []Issue
	Complete   bool
}

func (client Client) ReadProjectionIssuePage(ctx context.Context, owner, repository string, page int) (ProjectionIssuePage, error) {
	if owner == "" || repository == "" || page < 1 {
		return ProjectionIssuePage{}, fmt.Errorf("Issue snapshot owner, repository, and positive page are required")
	}
	ctx, finish := progress.Start(ctx, "projection", "issue_snapshot_page", slog.Int("page", page))
	defer finish(false)
	query := "repo:" + owner + "/" + repository + " is:issue"
	endpoint := "/search/issues?q=" + urlQuery(query) + "&per_page=100&page=" + strconv.Itoa(page)
	var response struct {
		TotalCount        int  `json:"total_count"`
		IncompleteResults bool `json:"incomplete_results"`
		Items             []struct {
			Number int    `json:"number"`
			NodeID string `json:"node_id"`
			URL    string `json:"html_url"`
			Body   string `json:"body"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"items"`
	}
	if err := client.rest(ctx, "GET", endpoint, nil, &response); err != nil {
		return ProjectionIssuePage{}, err
	}
	if response.IncompleteResults || response.TotalCount > 1000 {
		return ProjectionIssuePage{}, ErrProjectionSnapshotIncomplete
	}
	issues := make([]Issue, 0, len(response.Items))
	for _, item := range response.Items {
		labels := make([]string, 0, len(item.Labels))
		for _, label := range item.Labels {
			labels = append(labels, label.Name)
		}
		issues = append(issues, Issue{Number: item.Number, NodeID: item.NodeID, URL: item.URL, Body: item.Body, Labels: labels})
	}
	total := response.TotalCount
	if total == 0 && len(issues) > 0 {
		total = len(issues)
	}
	complete := len(issues) == 0 || page*100 >= total
	nextPage := 0
	if !complete {
		nextPage = page + 1
	}
	progress.Logger(ctx).InfoContext(ctx, "Issue snapshot page loaded", "issues", len(issues), "total_issues", total, "complete", complete)
	finish(true)
	return ProjectionIssuePage{Page: page, NextPage: nextPage, TotalCount: total, Issues: issues, Complete: complete}, nil
}

func (client Client) findIssueFromSnapshot(ctx context.Context, owner, repository, moduleID string) (Issue, error) {
	if client.MetadataCache == nil {
		return client.findIssueRemote(ctx, owner, repository, moduleID)
	}
	key := owner + "/" + repository
	index, found := client.MetadataCache.issueIndex(key)
	if !found {
		loaded, err := client.loadIssueIndex(ctx, owner, repository)
		if err != nil {
			return Issue{}, err
		}
		index = loaded
		client.MetadataCache.setIssueIndex(key, index)
	}
	matching := index[moduleID]
	if len(matching) > 1 {
		return Issue{}, fmt.Errorf("multiple Issues matched exact module marker %s", moduleID)
	}
	if len(matching) == 0 {
		return Issue{}, nil
	}
	return matching[0], nil
}

func (client Client) loadIssueIndex(ctx context.Context, owner, repository string) (map[string][]Issue, error) {
	index := map[string][]Issue{}
	for pageNumber := 1; ; pageNumber++ {
		page, err := client.ReadProjectionIssuePage(ctx, owner, repository, pageNumber)
		if err != nil {
			return nil, err
		}
		for _, issue := range page.Issues {
			if err = addIssueSnapshotEntry(index, issue); err != nil {
				return nil, err
			}
		}
		if err = client.MetadataCache.checkpointPage(ctx, page); err != nil {
			return nil, err
		}
		if page.Complete {
			return index, nil
		}
	}
}

func addIssueSnapshotEntry(index map[string][]Issue, issue Issue) error {
	markers := issueMarkerPattern.FindAllStringSubmatch(issue.Body, -1)
	if len(markers) == 0 {
		return nil
	}
	if len(markers) != 1 || !projectionModuleIDPattern.MatchString(markers[0][1]) {
		return fmt.Errorf("Issue %d has an ambiguous or invalid factory module marker", issue.Number)
	}
	moduleID := markers[0][1]
	index[moduleID] = append(index[moduleID], issue)
	return nil
}

func (cache *ProjectionMetadataCache) setSnapshotCheckpoint(head string, checkpoint func(context.Context, string, string) error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.currentStateHead = head
	cache.checkpointSnapshot = checkpoint
}

func (cache *ProjectionMetadataCache) checkpointPage(ctx context.Context, page ProjectionIssuePage) error {
	cursor := "complete"
	if !page.Complete {
		cursor = strconv.Itoa(page.NextPage)
	}
	return cache.checkpointCursor(ctx, "issues_page", cursor)
}

func (cache *ProjectionMetadataCache) checkpointCursor(ctx context.Context, name, cursor string) error {
	cache.mu.Lock()
	checkpoint := cache.checkpointSnapshot
	cache.mu.Unlock()
	if checkpoint == nil {
		return nil
	}
	return checkpoint(ctx, name, cursor)
}

func (cache *ProjectionMetadataCache) updateStateHead(head string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.currentStateHead = head
}

func (cache *ProjectionMetadataCache) stateHead(fallback string) string {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.currentStateHead == "" {
		return fallback
	}
	return cache.currentStateHead
}

func (client Client) cachedProjectID(ctx context.Context, owner string, number int) (string, error) {
	if client.MetadataCache == nil {
		return client.projectID(ctx, owner, number)
	}
	key := owner + ":" + strconv.Itoa(number)
	if projectID, found := client.MetadataCache.project(key); found {
		progress.Logger(ctx).InfoContext(ctx, "Project identity reused", "reason", "run cache")
		return projectID, nil
	}
	projectID, err := client.projectID(ctx, owner, number)
	if err != nil {
		return "", err
	}
	client.MetadataCache.setProject(key, projectID)
	return projectID, nil
}

func (client Client) cachedProjectFields(ctx context.Context, projectID string) (map[string]fieldRef, error) {
	if client.MetadataCache == nil {
		return client.projectFields(ctx, projectID)
	}
	if fields, found := client.MetadataCache.projectFields(projectID); found {
		progress.Logger(ctx).InfoContext(ctx, "Project field definitions reused", "reason", "run cache")
		return fields, nil
	}
	fields, err := client.projectFields(ctx, projectID)
	if err != nil {
		return nil, err
	}
	client.MetadataCache.setProjectFields(projectID, fields)
	return fields, nil
}

func (cache *ProjectionMetadataCache) project(key string) (string, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, exists := cache.projects[key]
	return value, exists
}

func (cache *ProjectionMetadataCache) setProject(key, value string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.projects == nil {
		cache.projects = map[string]string{}
	}
	cache.projects[key] = value
}

func (cache *ProjectionMetadataCache) projectFields(projectID string) (map[string]fieldRef, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	fields, exists := cache.fields[projectID]
	return cloneFieldRefs(fields), exists
}

func (cache *ProjectionMetadataCache) setProjectFields(projectID string, fields map[string]fieldRef) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.fields == nil {
		cache.fields = map[string]map[string]fieldRef{}
	}
	cache.fields[projectID] = cloneFieldRefs(fields)
}

func (cache *ProjectionMetadataCache) issueIndex(repository string) (map[string][]Issue, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	index, exists := cache.issues[repository]
	return cloneIssueIndex(index), exists
}

func (cache *ProjectionMetadataCache) setIssueIndex(repository string, index map[string][]Issue) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.issues == nil {
		cache.issues = map[string]map[string][]Issue{}
	}
	cache.issues[repository] = cloneIssueIndex(index)
	if cache.repoLabels == nil {
		cache.repoLabels = map[string]map[string]bool{}
		cache.repoLabelKnown = map[string]map[string]bool{}
	}
	if cache.repoLabels[repository] == nil {
		cache.repoLabels[repository] = map[string]bool{}
		cache.repoLabelKnown[repository] = map[string]bool{}
	}
	for _, issues := range index {
		for _, issue := range issues {
			for _, label := range issue.Labels {
				cache.repoLabels[repository][label] = true
				cache.repoLabelKnown[repository][label] = true
			}
		}
	}
}

func cloneIssueIndex(source map[string][]Issue) map[string][]Issue {
	if source == nil {
		return nil
	}
	cloned := make(map[string][]Issue, len(source))
	for moduleID, issues := range source {
		cloned[moduleID] = append([]Issue(nil), issues...)
		for position := range cloned[moduleID] {
			cloned[moduleID][position].Labels = append([]string(nil), issues[position].Labels...)
		}
	}
	return cloned
}

func (cache *ProjectionMetadataCache) repoLabel(repository, label string) (bool, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	known := cache.repoLabelKnown[repository][label]
	return cache.repoLabels[repository][label], known
}

func (cache *ProjectionMetadataCache) setRepoLabel(repository, label string, exists bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.repoLabels == nil {
		cache.repoLabels = map[string]map[string]bool{}
		cache.repoLabelKnown = map[string]map[string]bool{}
	}
	if cache.repoLabels[repository] == nil {
		cache.repoLabels[repository] = map[string]bool{}
		cache.repoLabelKnown[repository] = map[string]bool{}
	}
	cache.repoLabels[repository][label] = exists
	cache.repoLabelKnown[repository][label] = true
}

func cloneFieldRefs(fields map[string]fieldRef) map[string]fieldRef {
	if fields == nil {
		return nil
	}
	cloned := make(map[string]fieldRef, len(fields))
	for name, field := range fields {
		options := make(map[string]string, len(field.Options))
		for optionName, optionID := range field.Options {
			options[optionName] = optionID
		}
		cloned[name] = fieldRef{ID: field.ID, Options: options}
	}
	return cloned
}

func validateProjectMetadataCache(cache *ProjectionMetadataCache) error {
	if cache == nil {
		return fmt.Errorf("projection metadata cache is required")
	}
	return nil
}
