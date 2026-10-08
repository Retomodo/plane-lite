# Generates internal/mail/testdata/issue_updates.json: contexts for the
# notification digest template (emails/notifications/issue-updates.html)
# and the HTML Django renders from them, to check mail.RenderDjango. Run
# inside the reference stack:
#   scripts/devstack.sh N shell < contract/reference/gen_issue_updates_fixtures.py \
#     | sed -n 's/^FIXTURES//p' > internal/mail/testdata/issue_updates.json
import json
from django.template.loader import render_to_string


def actor(first, last, avatar="http://localhost:3000None"):
    return {"avatar_url": avatar, "first_name": first, "last_name": last}


def base(**kw):
    ctx = {
        "data": [],
        "summary": "Updates were made to the issue by",
        "actors_involved": 1,
        "issue": {"issue_identifier": "PL-1", "name": "Fix <the> \"bug\" & 'more'", "issue_url": "http://x/acme/i/1"},
        "receiver": {"email": "bob@example.com"},
        "issue_url": "http://x/acme/i/1",
        "project_url": "http://x/acme/p/",
        "workspace": "acme",
        "project": "Plane & Lite",
        "user_preference": "http://x/acme/settings/account/notifications/",
        "comments": [],
        "entity_type": "issue",
    }
    ctx.update(kw)
    return ctx


def update(changes, who=None, time="14:05 PM"):
    return {
        "actor_detail": who or actor("Alice", "Admin"),
        "changes": changes,
        "issue_details": {"name": "Fix", "identifier": "PL-1"},
        "activity_time": time,
    }


cases = [
    base(),
    base(data=[update({"name": {"old_value": ["Old"], "new_value": ["New <name>"]}})]),
    base(actors_involved=2, data=[update({
        "target_date": {"old_value": ["None"], "new_value": ["2030-01-15"]},
        "duplicate": {"old_value": ["PL-7", "PL-8", "PL-9"], "new_value": ["PL-2", "PL-3", "PL-4", "PL-5"]},
        "assignees": {"old_value": ["carol", "dan"], "new_value": ["bob", "erin", "fay"]},
        "labels": {"new_value": ["Bug"]},
        "state": {"old_value": ["Backlog"], "new_value": ["Todo", "Done"]},
        "link": {"old_value": ["None"], "new_value": ["http://a/1", "http://a/2?x=1&y=2"]},
        "priority": {"old_value": ["urgent"], "new_value": ["medium", "high"]},
        "blocking": {"old_value": ["PL-9"], "new_value": ["PL-2", "PL-3", "PL-4"]},
        "description": {"old_value": ["a"], "new_value": ["b"]},
    }, who=actor("", "", avatar=""))]),
    base(data=[update({
        "target_date": {"old_value": ["2030-01-15"], "new_value": ["None"]},
        "duplicate": {"new_value": ["PL-2"]},
        "assignees": {"old_value": ["carol"]},
        "labels": {"old_value": ["Bug", "Feature"], "new_value": ["Docs", "UI"]},
        "state": {"old_value": ["In Progress"], "new_value": ["Cancelled"]},
        "link": {"old_value": ["http://gone"], "new_value": []},
        "priority": {"old_value": ["low"], "new_value": ["urgent"]},
        "blocking": {"old_value": ["PL-9", "PL-10", "PL-11"]},
    })]),
    base(data=[update({"state": {"old_value": ["Custom"], "new_value": ["Done"]},
                       "priority": {"old_value": ["high"], "new_value": ["low"]}}),
               update({"state": {"old_value": ["Done"], "new_value": ["In Progress"]},
                       "priority": {"old_value": ["medium"], "new_value": ["none"]}}, who=actor("Bob", "B"))],
         actors_involved=2),
    base(comments=[{"actor_comments": {"new_value": ["<p>Hi <b>there</b> &amp; you</p>", "<p>two</p>"]},
                    "actor_detail": actor("Bob", "Builder", avatar="")},
                   {"actor_comments": {"new_value": None, "old_value": None}, "actor_detail": actor("Carol", "C")}],
         actors_involved=2),
    base(data=[update({"labels": {"new_value": ["A"]}})],
         comments=[{"actor_comments": {"old_value": ["<p>x</p>"]}, "actor_detail": actor("Dan", "")}]),
]

out = [{"context": c, "html": render_to_string("emails/notifications/issue-updates.html", c)} for c in cases]
print("FIXTURES" + json.dumps(out))
