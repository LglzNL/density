package report

import (
	"fmt"
	"strings"
	"time"
)

// SimpleMarkdownHeader returns a small standardized header.
// (MVP helper: später erweitern.)
func SimpleMarkdownHeader(project string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s\n\n", project))
	b.WriteString(fmt.Sprintf("_Generiert am %s_\n\n", time.Now().Format(time.RFC3339)))
	return b.String()
}
