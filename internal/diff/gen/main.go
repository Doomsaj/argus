// Command gen prints every oasdiff rule id of the pinned oasdiff version, one per line.
// Regenerate the inventory after an oasdiff bump: go run ./internal/diff/gen > internal/diff/policy/known-rules.txt
package main

import (
	"fmt"
	"slices"

	"github.com/oasdiff/oasdiff/checker"
)

func main() {
	ids := checker.GetAllRuleIds()
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		fmt.Println(id)
	}
}
