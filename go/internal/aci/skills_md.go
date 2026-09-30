package aci

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// SkillsFromMarkdownDir turns the plain-folder skills (skills/*.md) into skill
// definitions. Each file is one skill. An optional front-matter block supplies
// `name`, `description` and `requires` (comma-separated); otherwise the name
// is the file name and the description is the first line of prose. The
// markdown file itself becomes the skill's implementation (type "md"), so it
// is digested, signed and packed with the agent. The user's files are only read.
func SkillsFromMarkdownDir(dir string) ([]SkillDef, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "skills", "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var out []SkillDef
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(m)
		def, err := parseSkillMarkdown(strings.TrimSuffix(base, ".md"), string(data))
		if err != nil {
			return nil, fmt.Errorf("skills/%s: %w", base, err)
		}
		def.Impl = &SkillImpl{Type: "md", Entry: "skills/" + base}
		out = append(out, def)
	}
	return out, nil
}

func parseSkillMarkdown(stem, text string) (SkillDef, error) {
	def := SkillDef{Name: strings.ToLower(strings.ReplaceAll(stem, " ", "-")),
		Inputs: map[string]string{}, Outputs: map[string]string{}, Requires: []string{}}
	body := text
	if rest, ok := strings.CutPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "---\n"); ok {
		if fm, after, found := strings.Cut(rest, "\n---"); found {
			body = after
			for _, line := range strings.Split(fm, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				v = strings.Trim(strings.TrimSpace(v), `"'[]`)
				switch strings.TrimSpace(k) {
				case "name":
					def.Name = v
				case "description":
					def.Description = v
				case "requires":
					for _, r := range strings.Split(v, ",") {
						if r = strings.Trim(strings.TrimSpace(r), `"'`); r != "" {
							def.Requires = append(def.Requires, r)
						}
					}
				}
			}
		}
	}
	if def.Description == "" {
		for _, line := range strings.Split(body, "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				def.Description = line
				break
			}
		}
	}
	if !skillNameRe.MatchString(def.Name) {
		return def, fmt.Errorf("invalid skill name %q (use lowercase letters, digits, - and _)", def.Name)
	}
	return def, nil
}
