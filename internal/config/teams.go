package config

import (
	_ "embed"

	"gopkg.in/yaml.v3"
)

// Team is one entry of teams.yaml: the GitLab group whose members may approve waivers
// for this team, and (for GitHub demos without org teams) the GitHub logins that may.
type Team struct {
	GitLabGroup string   `yaml:"gitlabGroup"`
	GitHubUsers []string `yaml:"githubUsers"`
}

// teams.yaml maps argus team slugs (argus.yaml group / approvedBy) to approvers.
// Owned by the argus platform team; changes ship with an argus release.
//
//go:embed teams.yaml
var teamsYAML []byte

func Teams() (map[string]Team, error) {
	m := map[string]Team{}
	return m, yaml.Unmarshal(teamsYAML, &m)
}
