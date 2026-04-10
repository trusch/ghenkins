package runner

import "encoding/json"

// GenerateEventJSON creates the event payload JSON file content for act.
// eventType is "push" or "pull_request".
func GenerateEventJSON(repo, owner, repoName, sha, branch string, prNumber int, eventType string) ([]byte, error) {
	var payload any
	switch eventType {
	case "pull_request":
		payload = map[string]any{
			"action": "synchronize",
			"number": prNumber,
			"pull_request": map[string]any{
				"head":   map[string]any{"sha": sha},
				"number": prNumber,
			},
			"repository": map[string]any{
				"full_name": repo,
				"name":      repoName,
				"owner":     map[string]any{"login": owner},
			},
		}
	default: // "push"
		payload = map[string]any{
			"after": sha,
			"ref":   "refs/heads/" + branch,
			"repository": map[string]any{
				"full_name": repo,
				"name":      repoName,
				"owner":     map[string]any{"login": owner},
			},
			"head_commit": map[string]any{"id": sha},
		}
	}
	return json.Marshal(payload)
}
