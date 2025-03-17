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

const defaultPrPatchCoveragePath = "coverage/artifact-pr-patch-coverage-results.json"

type diffLineMapping struct {
	deletedStart int
	deletedEnd   int
	addedStart   int
	addedEnd     int
}

func (dlm diffLineMapping) String() string {
	var removedString, addedString string
	if dlm.deletedStart == dlm.deletedEnd {
		removedString = fmt.Sprintf("-%d,0", dlm.deletedStart)
	} else {
		count := dlm.deletedEnd - dlm.deletedStart
		if count == 1 {
			removedString = fmt.Sprintf("-%d", dlm.deletedStart+1)
		} else {
			removedString = fmt.Sprintf("-%d,%d", dlm.deletedStart+1, count)
		}
	}

	if dlm.addedStart == dlm.addedEnd {
		addedString = fmt.Sprintf("+%d,0", dlm.addedStart)
	} else {
		count := dlm.addedEnd - dlm.addedStart
		if count == 1 {
			addedString = fmt.Sprintf("-%d", dlm.addedStart+1)
		} else {
			addedString = fmt.Sprintf("+%d,%d", dlm.addedStart+1, count)
		}
	}

	return fmt.Sprintf("@@ %s %s @@", removedString, addedString)
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

		var deletedStart int
		if result["countBefore"] == 0 {
			deletedStart = result["lineBefore"]
		} else {
			deletedStart = result["lineBefore"] - 1
		}
		deletedEnd := deletedStart + result["countBefore"] + 1

		var addedStart int
		if result["countAfter"] == 0 {
			addedStart = result["lineAfter"]
		} else {
			addedStart = result["lineAfter"] - 1
		}
		addedEnd := addedStart + result["countAfter"] + 1

		dlm = diffLineMapping{
			deletedStart: deletedStart,
			deletedEnd:   deletedEnd,
			addedStart:   addedStart,
			addedEnd:     addedEnd,
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

func (fdm fileDiffMetadata) String() string {
	out := &strings.Builder{}
	out.WriteString("fileName: ")
	out.WriteString(fdm.fileName)
	out.WriteString("\n")
	for i, diffLine := range fdm.diffLines {
		out.WriteString(fmt.Sprintf("diffLines[%d]: %s\n", i, diffLine.String()))
	}
	return out.String()
}

func (fdm fileDiffMetadata) getAddedLineFor(deletedLine int) (int, error) {
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
		if deletedLine > dlm.deletedStart && deletedLine < dlm.deletedEnd {
			return -1, errors.Errorf("lineNo: %d cannot be mapped correctly as it is modified", deletedLine)
		}
		if deletedLine >= dlm.deletedEnd {
			validDiffLine = dlm
		}
	}

	if validDiffLine == (diffLineMapping{}) {
		return deletedLine, nil
	}

	mappedLineNo := validDiffLine.addedEnd + (deletedLine - validDiffLine.deletedEnd)
	return mappedLineNo, nil
}

func (fdm fileDiffMetadata) withinDeletedPatch(deletedLine int) bool {
	for _, dlm := range fdm.diffLines {
		if deletedLine > dlm.deletedStart && deletedLine < dlm.deletedEnd {
			return true
		}
	}
	return false
}

func (fdm fileDiffMetadata) withinAddedPatch(addedLine int) bool {
	for _, dlm := range fdm.diffLines {
		if addedLine > dlm.addedStart && addedLine < dlm.addedEnd {
			return true
		}
	}
	return false
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

func (p PrPatchGenerator) generatePatchReport(fromReport formatters.Report) (formatters.Report, error) {
	// Create a new report to store the patch coverage
	patchReport := formatters.Report{
		SourceFiles: formatters.SourceFiles{},
	}
	ignoredLine := formatters.NullInt{Int: -1, Valid: false}

	// Iterate over the source files in the report
	for _, sourceFile := range fromReport.SourceFiles {
		// Get the file name
		fileName := sourceFile.Name

		// Get the file diff metadata
		var prFileDiff fileDiffMetadata
		for _, f := range p.prFilesDiff {
			if f.fileName == fileName {
				prFileDiff = f
				break
			}
		}

		// If the file is not in the PR, skip it
		if prFileDiff.fileName == "" {
			continue
		}

		var prFileDiffLastMerge fileDiffMetadata
		for _, f := range p.prFilesDiffLastMerge {
			if f.fileName == fileName {
				prFileDiffLastMerge = f
				break
			}
		}

		// Getting file from git revision
		fileContent, err := loadFromGitRaw("show", fmt.Sprintf("%s:%s", p.headTipCommit, fileName))
		if err != nil {
			return patchReport, errors.WithStack(err)
		}
		fileContentLines := strings.Split(fileContent, "\n")

		// Create a new source file to store the patch coverage
		patchSourceFile := formatters.SourceFile{
			Name:     fileName,
			Coverage: make([]formatters.NullInt, len(fileContentLines)),
		}

		println(prFileDiff.String())

		// Setting
		success := true
		for i := range fileContentLines {
			lineNo := i + 1
			if prFileDiff.withinAddedPatch(lineNo) {
				fmt.Printf("[Patch check] fileName: %s, lineNo: %d\n", fileName, lineNo)
				afterLineNo, err := prFileDiffLastMerge.getAddedLineFor(lineNo)
				if err != nil {
					success = false
					break
				}
				patchSourceFile.Coverage[i] = fromReport.SourceFiles[fileName].Coverage[afterLineNo-1]
			} else {
				patchSourceFile.Coverage[i] = ignoredLine
			}
		}

		// Add the source file to the patch report
		if success {
			patchReport.SourceFiles[fileName] = patchSourceFile
		}
	}

	return patchReport, nil
}

var prPatchOptions = PrPatchGenerator{}

func getFileMappingFromDiff() error {
	for _, file := range prPatchOptions.prFiles {
		println("File: " + file)
		// Generating file diff between the mergeBaseCommit and headTipCommit
		println("diff between the mergeBaseCommit and headTipCommit :-")
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
		println("diff between the headTipCommit and lastMergeCommit :-")
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

		outRep, err := prPatchOptions.generatePatchReport(rep)
		if err != nil {
			return errors.WithStack(err)
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
	strResponse, err := loadFromGitRaw(gitArgs...)
	if err != nil {
		return "", errors.WithStack(err)
	}

	return strings.TrimSpace(strResponse), nil
}

func loadFromGitRaw(gitArgs ...string) (string, error) {
	cmd := exec.Command("git", gitArgs...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", errors.WithStack(err)
	}

	return string(out), nil
}

func init() {
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.baseBranch, "base-branch", "", "the base branch of the PR")
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.headBranch, "head-branch", "", "the head branch of the PR")
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.headTipCommit, "head-tip-commit", "", "commit on tip of PR head branch")
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.lastMergeCommit, "last-merge-commit", "", "last merge commit on the head branch")
	prPatchCoverageCmd.Flags().StringVar(&prPatchOptions.output, "output", defaultPrPatchCoveragePath, "output path")
	RootCmd.AddCommand(prPatchCoverageCmd)
}
