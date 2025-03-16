package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/codeclimate/test-reporter/env"
	"github.com/codeclimate/test-reporter/formatters"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

const defaultPrPatchCoveragePath = "coverage/patch_coverage.json"

type diffLineMapping struct {
	beforeLineA int
	beforeLineB int
	afterLineA  int
	afterLineB  int
}

func getDiffLineMapping(fromString string) (diffLineMapping, error) {
	lineNumberRegex := regexp.MustCompile(`@@ -(?P<lineBefore>\d+),?(?P<countBefore>\d+)? \+(?P<lineAfter>\d+),?(?P<countAfter>\d+)? @@`)
	var dlm diffLineMapping
	matches := lineNumberRegex.FindStringSubmatch(fromString)
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

		var beforeLineA int
		if result["countBefore"] == 0 {
			beforeLineA = result["lineBefore"]
		} else {
			beforeLineA = result["lineBefore"] - 1
		}
		beforeLineB := beforeLineA + result["countBefore"] + 1

		var afterLineA int
		if result["countAfter"] == 0 {
			afterLineA = result["lineAfter"]
		} else {
			afterLineA = result["lineAfter"] - 1
		}
		afterLineB := beforeLineA + result["countAfter"] + 1

		dlm = diffLineMapping{
			beforeLineA: beforeLineA,
			beforeLineB: beforeLineB,
			afterLineA:  afterLineA,
			afterLineB:  afterLineB,
		}
	} else {
		err := errors.Errorf("Unable to get the line numbers for line: '%s'", fromString)
		return dlm, err
	}
	return dlm, nil
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
		if lineNo > dlm.beforeLineA && lineNo < dlm.beforeLineB {
			return -1, errors.Errorf("lineNo: %d cannot be mapped correctly as it is modified", lineNo)
		}
		if lineNo >= dlm.beforeLineB {
			validDiffLine = dlm
		}
	}

	if validDiffLine == (diffLineMapping{}) {
		return lineNo, nil
	}

	mappedLineNo := validDiffLine.afterLineB + (lineNo - validDiffLine.beforeLineB)
	return mappedLineNo, nil
}

func getFileDiffMetadata(file string, fromDiffString string) (fileDiffMetadata, error) {
	fdm := fileDiffMetadata{fileName: file}
	for _, line := range strings.Split(fromDiffString, "\n") {
		if strings.Contains(line, "@@") {
			if dlm, err := getDiffLineMapping(line); err == nil {
				fdm.diffLines = append(fdm.diffLines, dlm)
			} else {
				return fdm, err
			}
		}
	}
	return fdm, nil
}

type PrPatchGenerator struct {
	baseBranch           string
	headBranch           string
	headTipCommit        string
	lastMergeCommit      string
	mergeBaseCommit      string
	output               string
	prFiles              []string
	prFilesDiff          []fileDiffMetadata
	prFilesDiffLastMerge []fileDiffMetadata
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

func getFileMappingFromDiff() error {
	for _, file := range prPatchOptions.prFiles {
		println("File: " + file)
		// Generating file diff between the mergeBaseCommit and headTipCommit
		diff, err := loadFromGit("diff", "-U0", prPatchOptions.mergeBaseCommit, prPatchOptions.headTipCommit, "--", file)
		if err != nil {
			return err
		}

		if fdm, err := getFileDiffMetadata(file, diff); err == nil {
			prPatchOptions.prFilesDiff = append(prPatchOptions.prFilesDiff, fdm)
		} else {
			return err
		}

		// Generating file diff between the headTipCommit and lastMergeCommit
		diff, err = loadFromGit("diff", "-U0", prPatchOptions.headTipCommit, prPatchOptions.lastMergeCommit, "--", file)
		if err != nil {
			return err
		}

		if fdm, err := getFileDiffMetadata(file, diff); err == nil {
			prPatchOptions.prFilesDiffLastMerge = append(prPatchOptions.prFilesDiffLastMerge, fdm)
		} else {
			return err
		}
	}
	return nil
}

func updatePrPatchOptions() error {
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
	println(prPatchOptions.String())

	if err := getFileMappingFromDiff(); err != nil {
		return err
	}

	return nil
}

var prPatchCoverageCmd = &cobra.Command{
	Use:   "pr-patch-coverage",
	Short: "Generates patch coverage for PR. Needs to be run after format-coverage",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := updatePrPatchOptions(); err != nil {
			return errors.WithStack(err)
		}

		rep := formatters.Report{
			SourceFiles: formatters.SourceFiles{},
		}

		f, err := os.Open(args[0])
		if err != nil {
			return errors.WithStack(err)
		}

		err = json.NewDecoder(f).Decode(&rep)
		if err != nil {
			return errors.WithStack(err)
		}

		outRep := formatters.Report{
			SourceFiles: formatters.SourceFiles{},
		}

		out, err := writer(prPatchOptions.output)
		if err != nil {
			return errors.WithStack(err)
		}

		err = outRep.Save(out)
		if err != nil {
			return errors.WithStack(err)
		}

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
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.output, "output", defaultPrPatchCoveragePath, "output path")
	RootCmd.AddCommand(prPatchCoverageCmd)
}
