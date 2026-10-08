package api

import "context"

// The issue_activity events of the attachment views:
// create_attachment_activity and delete_attachment_activity.

// attachmentActivity dispatches attachment.activity.created / .deleted.
func (t *activityTask) attachmentActivity(context.Context) error {
	switch t.j.Type {
	case "attachment.activity.created":
		t.current = loadDict(t.j.CurrentInstance)
		newValue, err := dictGet(t.current, "asset", strp(""))
		if err != nil {
			return err
		}
		id, err := dictUUID(t.current, "id")
		if err != nil {
			return err
		}
		t.add(&activity{Comment: "created an attachment", Verb: "created", Field: strp("attachment"),
			NewValue: newValue, NewIdentifier: id})
	case "attachment.activity.deleted":
		t.add(&activity{Comment: "deleted the attachment", Verb: "deleted", Field: strp("attachment")})
	}
	return nil
}
