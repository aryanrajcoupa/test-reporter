package cmd

import (
	"os"
	"os/exec"
	"strings"

	"github.com/codeclimate/test-reporter/env"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type PrPatchGenerator struct {
	baseBranch      string
	headBranch      string
	headTipCommit   string
	lastMergeCommit string
	mergeBaseCommit string
	prFiles         []string
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

var prPatchCoverageCmd = &cobra.Command{
	Use:   "pr-patch-coverage",
	Short: "Generates patch coverage for PR. Needs to be run after format-coverage",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := updatePrPatchOptions(args); err != nil {
			return err
		}
		println(prPatchOptions.String())
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
