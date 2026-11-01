package tools

import (
	"strings"

	"github.com/lalternativefabrique/cortex/shell"
)

type stage = shell.Stage

func parseShell(line string) ([]stage, error) { return shell.Parse(line) }

// stagesOf parses the line for the rules. A line the parser refuses is
// opaque: it is matched as one stage of its raw text, so a deny or an ask
// rule still sees it, and nothing allows it.
func stagesOf(line string) []stage {
	stages, err := shell.Parse(line)
	if err != nil {
		return []stage{{Words: strings.Fields(line)}}
	}
	for i := range stages {
		stages[i] = stages[i].Command()
	}
	return stages
}
