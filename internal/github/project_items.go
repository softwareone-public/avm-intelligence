package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/progress"
)

// ProjectItemSnapshot records the current Project-owned values for one Issue.
type ProjectItemSnapshot struct {
	ID     string
	Number int
	Values map[string]string
}

type projectItemsPage struct {
	Items       []ProjectItemSnapshot
	HasNextPage bool
	EndCursor   string
}

func nullableCursor(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (client Client) loadProjectItems(ctx context.Context, projectID string) error {
	if client.MetadataCache == nil || client.MetadataCache.hasProjectItems(projectID) {
		progress.Logger(ctx).InfoContext(ctx, "Project item snapshot skipped", "reason", "not needed or already cached")
		return nil
	}
	items, err := client.readAllProjectItems(ctx, projectID)
	if err != nil {
		return err
	}
	client.MetadataCache.setProjectItems(projectID, items)
	return nil
}

func (client Client) readAllProjectItems(ctx context.Context, projectID string) (map[int]ProjectItemSnapshot, error) {
	ctx, finish := progress.Start(ctx, "projection", "project_items_snapshot")
	defer finish(false)
	items := make(map[int]ProjectItemSnapshot)
	after := ""
	pageNumber := 0
	for {
		pageNumber++
		progress.Logger(ctx).InfoContext(ctx, "Project item page started", "page", pageNumber)
		page, err := client.readProjectItemsPage(ctx, projectID, after)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.Number != 0 {
				items[item.Number] = item
			}
		}
		nextCursor, complete, cursorErr := nextProjectItemsCursor(page)
		if cursorErr != nil {
			return nil, cursorErr
		}
		progress.Logger(ctx).InfoContext(ctx, "Project item page loaded", "page", pageNumber, "items", len(page.Items), "complete", complete)
		if err = client.MetadataCache.checkpointCursor(ctx, "project_items", nextCursor); err != nil {
			return nil, err
		}
		if complete {
			break
		}
		after = nextCursor
	}
	finish(true)
	return items, nil
}

func nextProjectItemsCursor(page projectItemsPage) (string, bool, error) {
	if !page.HasNextPage {
		return "complete", true, nil
	}
	if page.EndCursor == "" {
		return "", false, ErrProjectionSnapshotIncomplete
	}
	return page.EndCursor, false, nil
}

func (client Client) cachedProjectionMatches(ctx context.Context, owner string, projectNumber int, issue Issue, projection Projection) (bool, error) {
	projectID, err := client.cachedProjectID(ctx, owner, projectNumber)
	if err != nil {
		return false, err
	}
	fields, err := client.cachedProjectFields(ctx, projectID)
	if err != nil {
		return false, err
	}
	if err = client.loadProjectItems(ctx, projectID); err != nil {
		return false, err
	}
	item, exists := client.MetadataCache.projectItem(projectID, issue.Number)
	if !exists || !projectFieldsMatch(fields, item.Values, projection) || !factoryLabelsMatch(issue.Labels, Labels(projection)) {
		return false, nil
	}
	return true, nil
}

func projectFieldsMatch(fields map[string]fieldRef, current map[string]string, projection Projection) bool {
	for name, value := range desiredProjectFieldValues(projection) {
		field, exists := fields[name]
		if !exists {
			return false
		}
		if len(field.Options) > 0 {
			if _, exists = field.Options[value]; !exists {
				return false
			}
		}
		if current[name] != value {
			return false
		}
	}
	return true
}

func factoryLabelsMatch(existing, desired []string) bool {
	existingManaged := make(map[string]bool)
	for _, label := range existing {
		if strings.HasPrefix(label, "factory:") || strings.HasPrefix(label, "avm:") {
			existingManaged[label] = true
		}
	}
	if len(existingManaged) != len(desired) {
		return false
	}
	for _, label := range desired {
		if !existingManaged[label] {
			return false
		}
	}
	return true
}

func (client Client) readProjectItemsPage(ctx context.Context, projectID, after string) (projectItemsPage, error) {
	payload := map[string]any{
		"query":     `query($id:ID!,$after:String){node(id:$id){... on ProjectV2{items(first:100,after:$after){nodes{id content{... on Issue{number}} fieldValues(first:50){nodes{... on ProjectV2ItemFieldSingleSelectValue{field{... on ProjectV2FieldCommon{name}} name}... on ProjectV2ItemFieldTextValue{field{... on ProjectV2FieldCommon{name}} text}... on ProjectV2ItemFieldDateValue{field{... on ProjectV2FieldCommon{name}} date}}}} pageInfo{hasNextPage endCursor}}}}}`,
		"variables": map[string]any{"id": projectID, "after": nullableCursor(after)},
	}
	var response struct {
		Node struct {
			Items struct {
				Nodes []struct {
					ID      string `json:"id"`
					Content struct {
						Number int `json:"number"`
					} `json:"content"`
					FieldValues struct {
						Nodes []struct {
							Field struct {
								Name string `json:"name"`
							} `json:"field"`
							Name string `json:"name"`
							Text string `json:"text"`
							Date string `json:"date"`
						} `json:"nodes"`
					} `json:"fieldValues"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"items"`
		} `json:"node"`
	}
	if err := client.graph(ctx, payload, &response); err != nil {
		return projectItemsPage{}, err
	}
	page := projectItemsPage{HasNextPage: response.Node.Items.PageInfo.HasNextPage, EndCursor: response.Node.Items.PageInfo.EndCursor}
	for _, node := range response.Node.Items.Nodes {
		item := ProjectItemSnapshot{ID: node.ID, Number: node.Content.Number, Values: map[string]string{}}
		if item.Number != 0 {
			if err := validateProjectItem(item); err != nil {
				return projectItemsPage{}, err
			}
		}
		for _, value := range node.FieldValues.Nodes {
			switch {
			case value.Name != "":
				item.Values[value.Field.Name] = value.Name
			case value.Text != "":
				item.Values[value.Field.Name] = value.Text
			case value.Date != "":
				item.Values[value.Field.Name] = value.Date
			}
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func (cache *ProjectionMetadataCache) hasProjectItems(projectID string) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	_, exists := cache.items[projectID]
	return exists
}

func (cache *ProjectionMetadataCache) projectItem(projectID string, issueNumber int) (ProjectItemSnapshot, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	item, exists := cache.items[projectID][issueNumber]
	return cloneProjectItem(item), exists
}

func (cache *ProjectionMetadataCache) projectItemByID(projectID, itemID string) (ProjectItemSnapshot, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, item := range cache.items[projectID] {
		if item.ID == itemID {
			return cloneProjectItem(item), true
		}
	}
	return ProjectItemSnapshot{}, false
}

func (cache *ProjectionMetadataCache) setProjectItems(projectID string, items map[int]ProjectItemSnapshot) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.items == nil {
		cache.items = map[string]map[int]ProjectItemSnapshot{}
	}
	cloned := make(map[int]ProjectItemSnapshot, len(items))
	for number, item := range items {
		cloned[number] = cloneProjectItem(item)
	}
	cache.items[projectID] = cloned
}

func (cache *ProjectionMetadataCache) cacheProjectItem(projectID string, item ProjectItemSnapshot) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.items == nil {
		cache.items = map[string]map[int]ProjectItemSnapshot{}
	}
	if cache.items[projectID] == nil {
		cache.items[projectID] = map[int]ProjectItemSnapshot{}
	}
	cache.items[projectID][item.Number] = cloneProjectItem(item)
}

func (cache *ProjectionMetadataCache) setProjectItemValue(projectID, itemID, fieldName, value string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for number, item := range cache.items[projectID] {
		if item.ID == itemID {
			if item.Values == nil {
				item.Values = map[string]string{}
			}
			item.Values[fieldName] = value
			cache.items[projectID][number] = item
			return
		}
	}
}

func cloneProjectItem(item ProjectItemSnapshot) ProjectItemSnapshot {
	cloned := item
	cloned.Values = make(map[string]string, len(item.Values))
	for name, value := range item.Values {
		cloned.Values[name] = value
	}
	return cloned
}

func validateProjectItem(item ProjectItemSnapshot) error {
	if item.ID == "" || item.Number < 1 {
		return fmt.Errorf("project item snapshot identity is invalid")
	}
	return nil
}
