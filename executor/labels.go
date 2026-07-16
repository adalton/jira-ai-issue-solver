package executor

import (
	"errors"
	"fmt"

	"go.uber.org/zap"

	"jira-ai-issue-solver/models"
)

// setPipelineLabel applies the given label to a ticket and removes all
// other configured pipeline labels (both failure and lifecycle groups).
// This enforces mutual exclusivity across both groups — a ticket should
// have exactly one pipeline label at any time. If targetLabel is empty,
// only clears the others. All operations are best-effort: errors are
// logged but never propagated.
func (p *Pipeline) setPipelineLabel(
	logger *zap.Logger,
	ticketKey string,
	allLabels []string,
	targetLabel string,
) {
	for _, label := range allLabels {
		if label != "" && label != targetLabel {
			if err := p.tracker.RemoveLabel(ticketKey, label); err != nil {
				logger.Debug("Failed to remove pipeline label",
					zap.String("label", label), zap.Error(err))
			}
		}
	}

	if targetLabel != "" {
		if err := p.tracker.AddLabel(ticketKey, targetLabel); err != nil {
			logger.Warn("Failed to add pipeline label",
				zap.String("label", targetLabel), zap.Error(err))
		}
	}
}

// validateForkMode checks that fork-mode projects have a resolved
// GitHub username for the ticket assignee. Returns an error and
// applies the ForkUserMissing label when the mapping is missing.
// No-op for direct-mode projects.
func (p *Pipeline) validateForkMode(
	logger *zap.Logger,
	ticketKey string,
	workItem *models.WorkItem,
	settings *models.ProjectSettings,
) error {
	if !settings.ForkMode || settings.GitHubUsername != "" {
		return nil
	}

	assigneeDesc := "unassigned"
	if workItem.Assignee != nil {
		assigneeDesc = workItem.Assignee.Email
	}

	if settings.FailureLabels.ForkUserMissing != "" {
		allLabels := models.AllPipelineLabels(settings.FailureLabels, settings.LifecycleLabels)
		p.setPipelineLabel(logger, ticketKey, allLabels, settings.FailureLabels.ForkUserMissing)
	}

	if !settings.DisableErrorComments {
		detail := fmt.Sprintf(
			"fork mode requires assignee GitHub mapping: ticket assignee %s has no entry in jira.assignee_to_github_username",
			assigneeDesc,
		)
		comment := fmt.Sprintf(
			"%s Fork mode validation failed\n\nError: %s\n\nAdd the assignee's GitHub username to jira.assignee_to_github_username in the bot configuration.",
			statusCommentMarker, detail,
		)
		if err := p.tracker.AddComment(ticketKey, comment); err != nil {
			logger.Warn("Failed to post fork-mode error comment", zap.Error(err))
		}
	}

	return errors.New("fork mode requires assignee GitHub mapping: ticket assignee has no entry in jira.assignee_to_github_username")
}

// clearFailureLabels removes all configured failure labels from a
// ticket. Called on pipeline success paths to clean up labels from
// prior failed attempts. All operations are best-effort.
func (p *Pipeline) clearFailureLabels(
	logger *zap.Logger,
	ticketKey string,
	fl models.FailureLabels,
) {
	for _, label := range fl.All() {
		if label != "" {
			if err := p.tracker.RemoveLabel(ticketKey, label); err != nil {
				logger.Debug("Failed to remove failure label",
					zap.String("label", label), zap.Error(err))
			}
		}
	}
}
