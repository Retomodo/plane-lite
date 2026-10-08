package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 7 (comments, history, issue and comment reactions, subscribe). One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

// registerIssueDiscussionJobs has nothing to add: the comment and reaction
// events run through the issue_activity job (issue_activity_discussion.go).
func (a *API) registerIssueDiscussionJobs() {}

func (a *API) registerIssueDiscussionRoutes(rt *httpx.Router) {
	const p = projectPrefix
	// plane/app/urls/issue.py
	rt.Handle(p+"issues/{issue_id}/history/", httpx.Methods{
		"GET": a.projectEntityPerm(a.allowProject(anyRole, a.issueHistory)),
	})
	rt.Handle(p+"issues/{issue_id}/comments/", httpx.Methods{"POST": a.allowProject(anyRole, a.createComment)})
	rt.Handle(p+"issues/{issue_id}/comments/{pk}/", httpx.Methods{
		"PATCH":  a.allowComment(adminOnly, a.updateComment),
		"DELETE": a.allowComment(adminOnly, a.deleteComment),
	})
	rt.Handle(p+"issues/{issue_id}/subscribe/", httpx.Methods{
		"GET":    a.projectLitePerm(a.subscriptionStatus),
		"POST":   a.projectLitePerm(a.subscribe),
		"DELETE": a.projectLitePerm(a.unsubscribe),
	})
	rt.Handle(p+"issues/{issue_id}/reactions/", httpx.Methods{
		"GET":  a.listIssueReactions,
		"POST": a.allowProject(anyRole, a.createIssueReaction),
	})
	rt.Handle(p+"issues/{issue_id}/reactions/{reaction_code}/", httpx.Methods{
		"DELETE": a.allowProject(anyRole, a.deleteIssueReaction),
	})
	rt.Handle(p+"comments/{comment_id}/reactions/", httpx.Methods{
		"GET":  a.listCommentReactions,
		"POST": a.allowProject(anyRole, a.createCommentReaction),
	})
	rt.Handle(p+"comments/{comment_id}/reactions/{reaction_code}/", httpx.Methods{
		"DELETE": a.allowProject(anyRole, a.deleteCommentReaction),
	})
}
