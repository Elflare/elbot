package fileops

import (
	"fmt"
	"strings"
)

const maxDiffCells = 2_000_000

type DiffHunk struct {
	ops      []diffOp
	oldStart int
	newStart int
}

func UnifiedDiff(path string, oldLines, newLines []string, contextLines int) string {
	if diffTooLarge(oldLines, newLines) {
		return fmt.Sprintf("--- %s\n+++ %s\n# diff omitted: too many line comparisons (%d x %d)\n", path, path, len(oldLines), len(newLines))
	}
	ops := diffLines(oldLines, newLines)
	hunks := buildDiffHunks(ops, contextLines)
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", path)
	fmt.Fprintf(&b, "+++ %s\n", path)
	for _, hunk := range hunks {
		oldCount, newCount := diffHunkCounts(hunk.ops)
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", hunk.oldStart, oldCount, hunk.newStart, newCount)
		for _, op := range hunk.ops {
			switch op.kind {
			case diffEqual:
				fmt.Fprintf(&b, " %s\n", op.text)
			case diffDelete:
				fmt.Fprintf(&b, "-%s\n", op.text)
			case diffInsert:
				fmt.Fprintf(&b, "+%s\n", op.text)
			}
		}
	}
	return b.String()
}

func diffTooLarge(oldLines, newLines []string) bool {
	if len(oldLines) == 0 || len(newLines) == 0 {
		return false
	}
	return len(oldLines) > maxDiffCells/len(newLines)
}

type diffKind int

const (
	diffEqual diffKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffKind
	text string
}

func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := []diffOp{}
	for i, j := 0, 0; i < n || j < m; {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, diffOp{kind: diffEqual, text: a[i]})
			i++
			j++
		case j >= m || i < n && dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{kind: diffDelete, text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: diffInsert, text: b[j]})
			j++
		}
	}
	return ops
}

func buildDiffHunks(ops []diffOp, contextLines int) []DiffHunk {
	firstChange, lastChange := -1, -1
	for i, op := range ops {
		if op.kind != diffEqual {
			if firstChange == -1 {
				firstChange = i
			}
			lastChange = i
		}
	}
	if firstChange == -1 {
		return nil
	}
	var ranges [][2]int
	start := firstChange
	last := firstChange
	for i := firstChange + 1; i <= lastChange; i++ {
		if ops[i].kind == diffEqual {
			continue
		}
		if equalOpsBetween(ops, last+1, i) > contextLines*2 {
			ranges = append(ranges, [2]int{start, last})
			start = i
		}
		last = i
	}
	ranges = append(ranges, [2]int{start, last})
	hunks := make([]DiffHunk, 0, len(ranges))
	for _, r := range ranges {
		hunks = append(hunks, trimDiffContextRange(ops, r[0], r[1], contextLines))
	}
	return hunks
}

func equalOpsBetween(ops []diffOp, start, end int) int {
	count := 0
	for i := start; i < end; i++ {
		if ops[i].kind == diffEqual {
			count++
		}
	}
	return count
}

func trimDiffContextRange(ops []diffOp, first, last, contextLines int) DiffHunk {
	start := first - contextLines
	if start < 0 {
		start = 0
	}
	end := last + contextLines + 1
	if end > len(ops) {
		end = len(ops)
	}
	oldStart, newStart := 1, 1
	for _, op := range ops[:start] {
		switch op.kind {
		case diffEqual:
			oldStart++
			newStart++
		case diffDelete:
			oldStart++
		case diffInsert:
			newStart++
		}
	}
	return DiffHunk{ops: ops[start:end], oldStart: oldStart, newStart: newStart}
}

func diffHunkCounts(ops []diffOp) (int, int) {
	oldCount, newCount := 0, 0
	for _, op := range ops {
		switch op.kind {
		case diffEqual:
			oldCount++
			newCount++
		case diffDelete:
			oldCount++
		case diffInsert:
			newCount++
		}
	}
	return oldCount, newCount
}
