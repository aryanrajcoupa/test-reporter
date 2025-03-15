package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/codeclimate/test-reporter/env"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type diffLineMapping struct {
	lineBefore  int
	countBefore int
	lineAfter   int
	countAfter  int
}

func (dlm diffLineMapping) getBeforeLines() (int, int) {
	var beforeLineA int
	if dlm.countBefore == 0 {
		beforeLineA = dlm.lineBefore
	} else {
		beforeLineA = dlm.lineBefore - 1
	}
	beforeLineB := beforeLineA + dlm.countBefore + 1
	return beforeLineA, beforeLineB
}

func (dlm diffLineMapping) getAfterLines() (int, int) {
	var afterLineA int
	if dlm.countAfter == 0 {
		afterLineA = dlm.lineAfter
	} else {
		afterLineA = dlm.lineAfter - 1
	}
	afterLineB := afterLineA + dlm.countAfter + 1
	return afterLineA, afterLineB
}

type fileDiffMetadata struct {
	fileName  string
	diffLines []diffLineMapping
}

func (fdm fileDiffMetadata) getLineAfterForLineBefore(lineNo int) (int, error) {
	// The diffLineMapping is the 1 less than the lower_bound of condition lineNo < diffLine.lineBefore
	// Eg. for following conditions,
	// 	 lineNo = 86
	//   diffLines = ["@@ -10,0 +11 @@", "@@ -32,0 +34,3 @@", "@@ -92,0 +97,2 @@"]
	// The lower_bound for the condition 86 < lineBefore = "@@ -92,0 +97,2 @@"
	// The value should be,
	//   validDiffLine = "@@ -32,0 +34,3 @@"
	//
	var validDiffLine diffLineMapping
	for _, dlm := range fdm.diffLines {
		beforeLineA, beforeLineB := dlm.getBeforeLines()

		if lineNo > beforeLineA && lineNo < beforeLineB {
			return -1, errors.Errorf("lineNo: %d cannot be mapped correctly as it is modified", lineNo)
		}
		if lineNo >= beforeLineB {
			validDiffLine = dlm
		}
	}

	if validDiffLine == (diffLineMapping{}) {
		return lineNo, nil
	}

	_, beforeLineB := validDiffLine.getBeforeLines()
	_, afterLineB := validDiffLine.getAfterLines()
	mappedLineNo := afterLineB + (lineNo - beforeLineB)
	return mappedLineNo, nil
}

type PrPatchGenerator struct {
	baseBranch      string
	headBranch      string
	headTipCommit   string
	lastMergeCommit string
	mergeBaseCommit string
	prFiles         []string
	prFilesDiff     []fileDiffMetadata
}

func (p PrPatchGenerator) String() string {
	out := &strings.Builder{}
	out.WriteString("base-branch: ")
	out.WriteString(p.baseBranch)
	out.WriteString("\n")
	out.WriteString("head-branch: ")
	out.WriteString(p.headBranch)
	out.WriteString("\n")
	out.WriteString("head-tip-commit: ")
	out.WriteString(p.headTipCommit)
	out.WriteString("\n")
	out.WriteString("last-merge-commit: ")
	out.WriteString(p.lastMergeCommit)
	out.WriteString("\n")
	out.WriteString("merge-base-commit: ")
	out.WriteString(p.mergeBaseCommit)
	out.WriteString("\n")
	out.WriteString("pr-files: ")
	out.WriteString(strings.Join(p.prFiles, ", "))
	return out.String()
}

var prPatchOptions = PrPatchGenerator{}

func updatePrPatchOptions(args []string) error {
	if prPatchOptions.baseBranch == "" {
		return errors.New("base-branch is required")
	}
	if prPatchOptions.headBranch == "" {
		e, err := env.New()
		if err != nil {
			return err
		}
		prPatchOptions.headBranch = e.Git.Branch
	}
	if prPatchOptions.headTipCommit == "" {
		commit, err := loadFromGit("rev-parse", "origin/"+prPatchOptions.headBranch)
		if err != nil {
			return err
		}
		prPatchOptions.headTipCommit = commit
	}
	if prPatchOptions.lastMergeCommit == "" {
		commit, err := loadFromGit("rev-parse", prPatchOptions.headBranch)
		if err != nil {
			return err
		}
		prPatchOptions.lastMergeCommit = commit
	}
	// Get the merge base commit
	commit, err := loadFromGit("merge-base", prPatchOptions.baseBranch, prPatchOptions.headTipCommit)
	if err != nil {
		return err
	}
	prPatchOptions.mergeBaseCommit = commit

	// Get the list of files changed in the PR
	files, err := loadFromGit("diff", "--name-only", prPatchOptions.mergeBaseCommit, prPatchOptions.headTipCommit)
	if err != nil {
		return err
	}
	prPatchOptions.prFiles = strings.Split(files, "\n")

	return nil
}

func getFileMappingFromDiff() error {
	headBranchOrigin := "origin/" + prPatchOptions.headBranch
	lineNumberRegex := regexp.MustCompile(`@@ -(?P<lineBefore>\d+),?(?P<countBefore>\d+)? \+(?P<lineAfter>\d+),?(?P<countAfter>\d+)? @@`)

	for _, file := range prPatchOptions.prFiles {
		diff, err := loadFromGit("diff", "-U0", headBranchOrigin, "HEAD", "--", file)
		if err != nil {
			return err
		}

		println("File: " + file)
		fdm := fileDiffMetadata{fileName: file}
		for _, line := range strings.Split(diff, "\n") {
			if strings.Contains(line, "@@") {
				matches := lineNumberRegex.FindStringSubmatch(line)
				if len(matches) >= 4 {
					// Get names of the capturing groups
					names := lineNumberRegex.SubexpNames()
					println(fmt.Sprintf("Matches: %v, Names: %v", matches, names))
					// Create a map of group names to their values
					result := make(map[string]int)
					for i, name := range names {
						// names[i] is always an empty string from SubexpNames
						if i != 0 && name != "" && i < len(matches) {
							// Convert string to integer, default to 1 if empty
							val := matches[i]
							if val == "" {
								val = "1"
							}
							num := 0
							fmt.Sscanf(val, "%d", &num)
							result[name] = num
						}
					}

					println(fmt.Sprintf("Line before: %d, Count before: %d, Line after: %d, Count after: %d",
						result["lineBefore"], result["countBefore"], result["lineAfter"], result["countAfter"]))
					fdm.diffLines = append(fdm.diffLines, diffLineMapping{
						lineBefore:  result["lineBefore"],
						countBefore: result["countBefore"],
						lineAfter:   result["lineAfter"],
						countAfter:  result["countAfter"],
					})
				}
			}
		}
		prPatchOptions.prFilesDiff = append(prPatchOptions.prFilesDiff, fdm)
	}
	return nil
}

var prPatchCoverageCmd = &cobra.Command{
	Use:   "pr-patch-coverage",
	Short: "Generates patch coverage for PR. Needs to be run after format-coverage",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := updatePrPatchOptions(args); err != nil {
			return err
		}
		println(prPatchOptions.String())

		if err := getFileMappingFromDiff(); err != nil {
			return err
		}

		lineNo := 0
		fmt.Sscanf(args[0], "%d", &lineNo)
		resultLineNo, err := prPatchOptions.prFilesDiff[0].getLineAfterForLineBefore(lineNo)
		if err != nil {
			return err
		}
		println("Diff mapping: %d", resultLineNo)

		return nil
	},
}

func loadFromGit(gitArgs ...string) (string, error) {
	cmd := exec.Command("git", gitArgs...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", errors.WithStack(err)
	}

	return strings.TrimSpace(string(out)), nil
}

func init() {
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.baseBranch, "base-branch", "", "the base branch of the PR")
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.headBranch, "head-branch", "", "the head branch of the PR")
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.headTipCommit, "head-tip-commit", "", "commit on tip of PR head branch")
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.lastMergeCommit, "last-merge-commit", "", "last merge commit on the head branch")
	RootCmd.AddCommand(prPatchCoverageCmd)
}
